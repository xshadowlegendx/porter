package stack

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/fatih/color"
	api "github.com/porter-dev/porter/api/client"
	"github.com/porter-dev/porter/api/types"
	"github.com/porter-dev/porter/cli/cmd/config"
)

type DeployStackHook struct {
	Client               *api.Client
	StackName            string
	ProjectID, ClusterID uint
	BuildImageDriverName string
	PorterYAML           []byte
}

func (t *DeployStackHook) PreApply() error {
	err := config.ValidateCLIEnvironment()
	if err != nil {
		errMsg := composePreviewMessage("porter CLI is not configured correctly", Error)
		return fmt.Errorf("%s: %w", errMsg, err)
	}
	return nil
}

func (t *DeployStackHook) DataQueries() map[string]interface{} {
	res := map[string]interface{}{
		"image": fmt.Sprintf("{$.%s.image}", t.BuildImageDriverName),
	}
	return res
}

// deploy the stack
func (t *DeployStackHook) PostApply(driverOutput map[string]interface{}) error {
	client := config.GetAPIClient()
	namespace := fmt.Sprintf("porter-stack-%s", t.StackName)

	_, err := client.GetRelease(
		context.Background(),
		t.ProjectID,
		t.ClusterID,
		namespace,
		t.StackName,
	)

	shouldCreate := err != nil

	if err != nil {
		color.New(color.FgYellow).Printf("Could not read release for stack %s (%s): attempting creation\n", t.StackName, err.Error())
	} else {
		color.New(color.FgGreen).Printf("Found release for stack %s: attempting update\n", t.StackName)
	}

	return t.applyStack(client, shouldCreate, driverOutput)
}

func (t *DeployStackHook) applyStack(client *api.Client, shouldCreate bool, driverOutput map[string]interface{}) error {
	var imageInfo types.ImageInfo
	image, ok := driverOutput["image"].(string)
	if ok && image != "" {
		// split image into image-path:tag format
		imageSpl := strings.Split(image, ":")
		imageInfo = types.ImageInfo{
			Repository: imageSpl[0],
			Tag:        imageSpl[1],
		}
	}

	if shouldCreate {
		err := client.CreateStack(
			context.Background(),
			t.ProjectID,
			t.ClusterID,
			&types.CreateStackReleaseRequest{
				StackName:        t.StackName,
				PorterYAMLBase64: base64.StdEncoding.EncodeToString(t.PorterYAML),
				ImageInfo:        imageInfo,
			},
		)
		if err != nil {
			return fmt.Errorf("error creating stack %s: %w", t.StackName, err)
		}
	} else {
		err := client.UpdateStack(
			context.Background(),
			t.ProjectID,
			t.ClusterID,
			t.StackName,
			&types.CreateStackReleaseRequest{
				StackName:        t.StackName,
				PorterYAMLBase64: base64.StdEncoding.EncodeToString(t.PorterYAML),
				ImageInfo:        imageInfo,
			},
		)
		if err != nil {
			return fmt.Errorf("error updating stack %s: %w", t.StackName, err)
		}
	}

	return nil
}

func (t *DeployStackHook) OnConsolidatedErrors(map[string]error) {}
func (t *DeployStackHook) OnError(error)                         {}