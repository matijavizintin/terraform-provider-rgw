package provider

import (
	"context"
	"errors"
	"fmt"
	"github.com/ceph/go-ceph/rgw/admin"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/smithy-go"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.ResourceWithConfigure = &BucketResource{}

func NewBucketResource() resource.Resource {
	return &BucketResource{}
}

type BucketResource struct {
	client *RgwClient
}

type BucketResourceModel struct {
	Id       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Location types.String `tfsdk:"location"`
}

func (r *BucketResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (r *BucketResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Bucket in Ceph RGW",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Example identifier",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Bucket Name",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"location": schema.StringAttribute{
				MarkdownDescription: "Bucket Location",
				Optional:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *BucketResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*RgwClient)

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *RgwClient, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)

		return
	}

	r.client = client
}

func (r *BucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Read Terraform plan data into the model
	var data *BucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Configure CreateBucketInput
	s3req := &s3.CreateBucketInput{
		Bucket: aws.String(data.Name.ValueString()),
	}
	if !data.Location.IsNull() {
		s3req.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(data.Location.ValueString()),
		}
	}

	tflog.Info(ctx, fmt.Sprintf("create bucket %s", *s3req.Bucket))

	_, err := r.client.S3.CreateBucket(ctx, s3req)
	if err != nil {
		resp.Diagnostics.AddError("could not create bucket", err.Error())
		return
	}

	data.Id = types.StringValue(*s3req.Bucket)

	// Write logs using the tflog package
	// Documentation: https://terraform.io/plugin/log
	tflog.Trace(ctx, "created a resource")

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *BucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Read Terraform prior state data into the model
	var data *BucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// get bucket zonegroup
	s3req := &s3.GetBucketLocationInput{
		Bucket: aws.String(data.Id.ValueString()),
	}

	s3resp, err := r.client.S3.GetBucketLocation(ctx, s3req)
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			switch ae.ErrorCode() {
			case "404":
				resp.State.RemoveResource(ctx)
				return
			case "403":
				resp.Diagnostics.AddError("no permission to get bucket", err.Error())
				return
			case "BucketNotEmpty":
				resp.Diagnostics.AddError("bucket needs to be empty", err.Error())
				return
			}
		}
		resp.Diagnostics.AddError("could not get bucket location", err.Error())
		return
	}

	// get bucket placement rule
	bucket := admin.Bucket{
		Bucket: data.Id.ValueString(),
	}
	bucket, err = r.client.Admin.GetBucketInfo(context.Background(), bucket)
	if err != nil {
		if errors.Is(err, admin.ErrNoSuchBucket) {
			// Remove bucket quota from state
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("could not get bucket quota", err.Error())
		return
	}

	location := fmt.Sprintf("%s:%s", s3resp.LocationConstraint, bucket.PlacementRule)

	data.Name = types.StringValue(*s3req.Bucket)
	data.Location = types.StringValue(location)

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *BucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Read Terraform plan data into the model
	var data *BucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Currently there is nothing to update in place

	// Save updated data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *BucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data *BucketResourceModel

	// Read Terraform prior state data into the model
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)

	if resp.Diagnostics.HasError() {
		return
	}

	s3req := &s3.DeleteBucketInput{
		Bucket: aws.String(data.Id.ValueString()),
	}

	_, err := r.client.S3.DeleteBucket(ctx, s3req)
	if err != nil {
		resp.Diagnostics.AddError("could not delete bucket", err.Error())
		return
	}
}
