package porter_app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/porter-dev/porter/internal/telemetry"

	"github.com/porter-dev/porter/api/server/authz"
	"github.com/porter-dev/porter/api/server/handlers"
	"github.com/porter-dev/porter/api/server/shared"
	"github.com/porter-dev/porter/api/server/shared/apierrors"
	"github.com/porter-dev/porter/api/server/shared/config"
	"github.com/porter-dev/porter/api/server/shared/requestutils"
	"github.com/porter-dev/porter/api/types"
	"github.com/porter-dev/porter/internal/helm"
	"github.com/porter-dev/porter/internal/helm/loader"
	"github.com/porter-dev/porter/internal/models"
	"github.com/porter-dev/porter/internal/repository"
	"github.com/stefanmcshane/helm/pkg/chart"
)

type CreatePorterAppHandler struct {
	handlers.PorterHandlerReadWriter
	authz.KubernetesAgentGetter
}

func NewCreatePorterAppHandler(
	config *config.Config,
	decoderValidator shared.RequestDecoderValidator,
	writer shared.ResultWriter,
) *CreatePorterAppHandler {
	return &CreatePorterAppHandler{
		PorterHandlerReadWriter: handlers.NewDefaultPorterHandler(config, decoderValidator, writer),
		KubernetesAgentGetter:   authz.NewOutOfClusterAgentGetter(config),
	}
}

func (c *CreatePorterAppHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project, _ := ctx.Value(types.ProjectScope).(*models.Project)
	cluster, _ := ctx.Value(types.ClusterScope).(*models.Cluster)

	ctx, span := telemetry.NewSpan(r.Context(), "serve-create-porter-app")
	defer span.End()

	request := &types.CreatePorterAppRequest{}
	if ok := c.DecodeAndValidate(w, r, request); !ok {
		c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error decoding request")))
		return
	}

	stackName, reqErr := requestutils.GetURLParamString(r, types.URLParamStackName)
	if reqErr != nil {
		c.HandleAPIError(w, r, apierrors.NewErrPassThroughToClient(reqErr, http.StatusBadRequest))
		return
	}
	namespace := fmt.Sprintf("porter-stack-%s", stackName)

	helmAgent, err := c.GetHelmAgent(ctx, r, cluster, namespace)
	if err != nil {
		c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error getting helm agent: %w", err)))
		return
	}

	k8sAgent, err := c.GetAgent(r, cluster, namespace)
	if err != nil {
		c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error getting k8s agent: %w", err)))
		return
	}

	helmRelease, err := helmAgent.GetRelease(ctx, stackName, 0, false)
	shouldCreate := err != nil

	porterYamlBase64 := request.PorterYAMLBase64
	porterYaml, err := base64.StdEncoding.DecodeString(porterYamlBase64)
	if err != nil {
		c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error decoding porter.yaml: %w", err)))
		return
	}
	imageInfo := request.ImageInfo
	registries, err := c.Repo().Registry().ListRegistriesByProjectID(cluster.ProjectID)
	if err != nil {
		c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error listing registries: %w", err)))
		return
	}

	var releaseValues map[string]interface{}
	var releaseDependencies []*chart.Dependency
	if shouldCreate || request.OverrideRelease {
		releaseValues = nil
		releaseDependencies = nil

		// this is required because when the front-end sends an update request with overrideRelease=true, it is unable to
		// get the image info from the release. unless it is explicitly provided in the request, we avoid overwriting it
		// by attempting to get the image info from the release
		if helmRelease != nil && (imageInfo.Repository == "" || imageInfo.Tag == "") {
			imageInfo = attemptToGetImageInfoFromRelease(helmRelease.Config)
		}
	} else {
		releaseValues = helmRelease.Config
		releaseDependencies = helmRelease.Chart.Metadata.Dependencies
	}

	if request.Builder == "" {
		// attempt to get builder from db
		app, err := c.Repo().PorterApp().ReadPorterAppByName(cluster.ID, stackName)
		if err == nil {
			request.Builder = app.Builder
		}
	}
	injectLauncher := strings.Contains(request.Builder, "heroku") ||
		strings.Contains(request.Builder, "paketo")

	chart, values, releaseJobValues, err := parse(
		porterYaml,
		imageInfo,
		c.Config(),
		cluster.ProjectID,
		releaseValues,
		releaseDependencies,
		SubdomainCreateOpts{
			k8sAgent:       k8sAgent,
			dnsRepo:        c.Repo().DNSRecord(),
			powerDnsClient: c.Config().PowerDNSClient,
			appRootDomain:  c.Config().ServerConf.AppRootDomain,
			stackName:      stackName,
		},
		injectLauncher,
	)
	if err != nil {
		c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error parsing porter.yaml into chart and values: %w", err)))
		return
	}

	if shouldCreate {
		// create the namespace if it does not exist already
		_, err = k8sAgent.CreateNamespace(namespace, nil)
		if err != nil {
			c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error creating namespace: %w", err)))
			return
		}

		// create the release job chart if it does not exist (only done by front-end currently, where we set overrideRelease=true)
		if request.OverrideRelease && releaseJobValues != nil {
			conf, err := createReleaseJobChart(
				ctx,
				stackName,
				releaseJobValues,
				c.Config().ServerConf.DefaultApplicationHelmRepoURL,
				registries,
				cluster,
				c.Repo(),
			)
			if err != nil {
				c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error making config for release job chart: %w", err)))
				return
			}
			_, err = helmAgent.InstallChart(ctx, conf, c.Config().DOConf, c.Config().ServerConf.DisablePullSecretsInjection)
			if err != nil {
				c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error creating release job chart: %w", err)))
				_, err = helmAgent.UninstallChart(ctx, fmt.Sprintf("%s-r", stackName))
				if err != nil {
					c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error uninstalling release job chart: %w", err)))
				}
				return
			}
		}

		conf := &helm.InstallChartConfig{
			Chart:      chart,
			Name:       stackName,
			Namespace:  namespace,
			Values:     values,
			Cluster:    cluster,
			Repo:       c.Repo(),
			Registries: registries,
		}

		// create the app chart
		_, err = helmAgent.InstallChart(ctx, conf, c.Config().DOConf, c.Config().ServerConf.DisablePullSecretsInjection)
		if err != nil {
			c.HandleAPIError(w, r, apierrors.NewErrPassThroughToClient(fmt.Errorf("error deploying app: %s", err.Error()), http.StatusBadRequest))

			_, err = helmAgent.UninstallChart(ctx, stackName)
			if err != nil {
				c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error uninstalling chart: %w", err)))
			}

			return
		}

		existing, err := c.Repo().PorterApp().ReadPorterAppByName(cluster.ID, stackName)
		if err != nil {
			c.HandleAPIError(w, r, apierrors.NewErrInternal(err))
			return
		} else if existing.Name != "" {
			c.HandleAPIError(w, r, apierrors.NewErrPassThroughToClient(
				fmt.Errorf("app with name %s already exists in your project", existing.Name), http.StatusForbidden))
			return
		}

		app := &models.PorterApp{
			Name:      stackName,
			ClusterID: cluster.ID,
			ProjectID: project.ID,
			RepoName:  request.RepoName,
			GitRepoID: request.GitRepoID,
			GitBranch: request.GitBranch,

			BuildContext:   request.BuildContext,
			Builder:        request.Builder,
			Buildpacks:     request.Buildpacks,
			Dockerfile:     request.Dockerfile,
			ImageRepoURI:   request.ImageRepoURI,
			PullRequestURL: request.PullRequestURL,
			PorterYamlPath: request.PorterYamlPath,
		}

		// create the db entry
		porterApp, err := c.Repo().PorterApp().UpdatePorterApp(app)
		if err != nil {
			c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error writing app to DB: %s", err.Error())))
			return
		}

		c.createPorterAppEvent(ctx, "SUCCESS", porterApp.ID, 1)

		c.WriteResult(w, r, porterApp.ToPorterAppType())
	} else {
		// create/update the release job chart
		if request.OverrideRelease {
			releaseJobName := fmt.Sprintf("%s-r", stackName)
			helmRelease, err := helmAgent.GetRelease(ctx, releaseJobName, 0, false)
			if err != nil {
				// here the user has chosen to create a release job for an already created app, so we need to create and install the release job chart
				conf, err := createReleaseJobChart(
					ctx,
					stackName,
					releaseJobValues,
					c.Config().ServerConf.DefaultApplicationHelmRepoURL,
					registries,
					cluster,
					c.Repo(),
				)
				if err != nil {
					c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error making config for release job chart: %w", err)))
					return
				}
				_, err = helmAgent.InstallChart(ctx, conf, c.Config().DOConf, c.Config().ServerConf.DisablePullSecretsInjection)
				if err != nil {
					c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error creating release job chart: %w", err)))
					_, err = helmAgent.UninstallChart(ctx, fmt.Sprintf("%s-r", stackName))
					if err != nil {
						c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error uninstalling release job chart: %w", err)))
					}
					return
				}
			} else {
				// release job exists, so we need to update it or delete it

				// here the release job exists, but now the user wants to delete it
				if releaseJobValues == nil {
					_, err = helmAgent.UninstallChart(ctx, releaseJobName)
					if err != nil {
						c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error uninstalling release job chart: %w", err)))
					}
					return
				} else {
					chart, err := loader.LoadChartPublic(ctx, c.Config().Metadata.DefaultAppHelmRepoURL, "job", "")
					if err != nil {
						c.HandleAPIError(w, r, apierrors.NewErrPassThroughToClient(fmt.Errorf("error loading release job chart: %w", err), http.StatusBadRequest))
						return
					}

					conf := &helm.UpgradeReleaseConfig{
						Name:       helmRelease.Name,
						Cluster:    cluster,
						Repo:       c.Repo(),
						Registries: registries,
						Values:     releaseJobValues,
						Chart:      chart,
					}
					_, err = helmAgent.UpgradeReleaseByValues(ctx, conf, c.Config().DOConf, c.Config().ServerConf.DisablePullSecretsInjection, false)
					if err != nil {
						c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error upgrading release job chart: %w", err)))
						return
					}
				}
			}
		}

		// update the app chart
		conf := &helm.InstallChartConfig{
			Chart:      chart,
			Name:       stackName,
			Namespace:  namespace,
			Values:     values,
			Cluster:    cluster,
			Repo:       c.Repo(),
			Registries: registries,
		}

		// update the chart
		_, err = helmAgent.UpgradeInstallChart(ctx, conf, c.Config().DOConf, c.Config().ServerConf.DisablePullSecretsInjection)
		if err != nil {
			c.HandleAPIError(w, r, apierrors.NewErrPassThroughToClient(fmt.Errorf("error deploying app: %s", err.Error()), http.StatusBadRequest))
			return
		}

		// update the DB entry
		app, err := c.Repo().PorterApp().ReadPorterAppByName(cluster.ID, stackName)
		if err != nil {
			c.HandleAPIError(w, r, apierrors.NewErrInternal(err))
			return
		}

		if request.RepoName != "" {
			app.RepoName = request.RepoName
		}
		if request.GitBranch != "" {
			app.GitBranch = request.GitBranch
		}
		if request.BuildContext != "" {
			app.BuildContext = request.BuildContext
		}
		if request.Builder != "" {
			if request.Builder == "null" {
				app.Builder = ""
			} else {
				app.Builder = request.Builder
			}
		}
		if request.Buildpacks != "" {
			if request.Buildpacks == "null" {
				app.Buildpacks = ""
			} else {
				app.Buildpacks = request.Buildpacks
			}
		}
		if request.Dockerfile != "" {
			if request.Dockerfile == "null" {
				app.Dockerfile = ""
			} else {
				app.Dockerfile = request.Dockerfile
			}
		}
		if request.ImageRepoURI != "" {
			app.ImageRepoURI = request.ImageRepoURI
		}
		if request.PullRequestURL != "" {
			app.PullRequestURL = request.PullRequestURL
		}

		updatedPorterApp, err := c.Repo().PorterApp().UpdatePorterApp(app)
		if err != nil {
			c.HandleAPIError(w, r, apierrors.NewErrInternal(fmt.Errorf("error writing updated app to DB: %s", err.Error())))
			return
		}

		c.createPorterAppEvent(ctx, "SUCCESS", updatedPorterApp.ID, helmRelease.Version+1)

		c.WriteResult(w, r, updatedPorterApp.ToPorterAppType())
	}
}

// createPorterAppEvent creates an event for use in the activity feed
func (c *CreatePorterAppHandler) createPorterAppEvent(ctx context.Context, status string, appID uint, revision int) (*models.PorterAppEvent, error) {
	event := models.PorterAppEvent{
		ID:                 uuid.New(),
		Status:             status,
		Type:               "DEPLOY",
		TypeExternalSource: "KUBERNETES",
		PorterAppID:        appID,
		Metadata: map[string]any{
			"revision": revision,
		},
	}

	err := c.Repo().PorterAppEvent().CreateEvent(ctx, &event)
	if err != nil {
		return nil, err
	}

	if event.ID == uuid.Nil {
		return nil, err
	}

	return &event, nil
}

func createReleaseJobChart(
	ctx context.Context,
	stackName string,
	values map[string]interface{},
	repoUrl string,
	registries []*models.Registry,
	cluster *models.Cluster,
	repo repository.Repository,
) (*helm.InstallChartConfig, error) {
	chart, err := loader.LoadChartPublic(ctx, repoUrl, "job", "")
	if err != nil {
		return nil, err
	}

	releaseName := fmt.Sprintf("%s-r", stackName)
	namespace := fmt.Sprintf("porter-stack-%s", stackName)

	return &helm.InstallChartConfig{
		Chart:      chart,
		Name:       releaseName,
		Namespace:  namespace,
		Values:     values,
		Cluster:    cluster,
		Repo:       repo,
		Registries: registries,
	}, nil
}