package list

import (
	"context"
	"fmt"
	http "github.com/microsoft/kiota-http-go"
	v1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/redhat-developer/app-services-cli/pkg/apisdk"
	"github.com/redhat-developer/app-services-cli/pkg/apisdk/models"
	"strconv"

	kafkaFlagutil "github.com/redhat-developer/app-services-cli/pkg/cmd/kafka/flagutil"
	"github.com/redhat-developer/app-services-cli/pkg/cmd/kafka/kafkacmdutil"

	"github.com/redhat-developer/app-services-cli/pkg/core/cmdutil"
	"github.com/redhat-developer/app-services-cli/pkg/core/cmdutil/flagutil"
	"github.com/redhat-developer/app-services-cli/pkg/core/ioutil/dump"
	"github.com/redhat-developer/app-services-cli/pkg/core/ioutil/icon"
	"github.com/redhat-developer/app-services-cli/pkg/core/localize"
	"github.com/redhat-developer/app-services-cli/pkg/shared/contextutil"
	"github.com/redhat-developer/app-services-cli/pkg/shared/factory"

	clustermgmt "github.com/redhat-developer/app-services-cli/pkg/shared/connection/api/clustermgmt"

	"github.com/spf13/cobra"

	"github.com/redhat-developer/app-services-cli/internal/build"

	authentication "github.com/microsoft/kiota-abstractions-go/authentication"
	u "net/url"
)

// row is the details of a Kafka instance needed to print to a table
type kafkaRow struct {
	ID               string `json:"id" header:"ID"`
	Name             string `json:"name" header:"Name"`
	Owner            string `json:"owner" header:"Owner"`
	Status           string `json:"status" header:"Status"`
	CloudProvider    string `json:"cloud_provider" header:"Cloud Provider"`
	Region           string `json:"region" header:"Region"`
	OpenshiftCluster string `json:"openshift_cluster" header:"Openshift Cluster"`
}

type options struct {
	outputFormat            string
	page                    int
	limit                   int
	search                  string
	accessToken             string
	clusterManagementApiUrl string

	f *factory.Factory
}

// NewListCommand creates a new command for listing kafkas.
func NewListCommand(f *factory.Factory) *cobra.Command {
	opts := &options{
		f: f,
	}

	cmd := &cobra.Command{
		Use:     "list",
		Short:   opts.f.Localizer.MustLocalize("kafka.list.cmd.shortDescription"),
		Long:    opts.f.Localizer.MustLocalize("kafka.list.cmd.longDescription"),
		Example: opts.f.Localizer.MustLocalize("kafka.list.cmd.example"),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.outputFormat != "" && !flagutil.IsValidInput(opts.outputFormat, flagutil.ValidOutputFormats...) {
				return flagutil.InvalidValueError("output", opts.outputFormat, flagutil.ValidOutputFormats...)
			}

			validator := &kafkacmdutil.Validator{
				Localizer: opts.f.Localizer,
			}

			if err := validator.ValidateSearchInput(opts.search); err != nil {
				return err
			}

			return runList(opts)
		},
	}

	flags := kafkaFlagutil.NewFlagSet(cmd, opts.f.Localizer)

	flags.AddOutput(&opts.outputFormat)
	flags.IntVar(&opts.page, "page", int(cmdutil.ConvertPageValueToInt32(build.DefaultPageNumber)), opts.f.Localizer.MustLocalize("kafka.list.flag.page"))
	flags.IntVar(&opts.limit, "limit", int(cmdutil.ConvertPageValueToInt32(build.DefaultPageSize)), opts.f.Localizer.MustLocalize("kafka.list.flag.limit"))
	flags.StringVar(&opts.search, "search", "", opts.f.Localizer.MustLocalize("kafka.list.flag.search"))
	flags.StringVar(&opts.clusterManagementApiUrl, "cluster-mgmt-api-url", "", f.Localizer.MustLocalize("dedicated.registerCluster.flag.clusterMgmtApiUrl.description"))
	flags.StringVar(&opts.accessToken, "access-token", "", f.Localizer.MustLocalize("dedicated.registercluster.flag.accessToken.description"))

	_ = flags.MarkHidden("cluster-mgmt-api-url")
	_ = flags.MarkHidden("access-token")

	return cmd
}

type RedHatAccessTokenProvider struct {
	accessToken string
}

func (r RedHatAccessTokenProvider) GetAuthorizationToken(context context.Context, url *u.URL, additionalAuthenticationContext map[string]interface{}) (string, error) {
	return r.accessToken, nil
}

func (r RedHatAccessTokenProvider) GetAllowedHostsValidator() *authentication.AllowedHostsValidator {
	return nil
}

func runList(opts *options) error {

	conn, err := opts.f.Connection()
	if err != nil {
		return err
	}

	api := conn.API()

	a := api.KafkaMgmt().GetKafkas(opts.f.Context)
	a = a.Page(strconv.Itoa(opts.page))
	a = a.Size(strconv.Itoa(opts.limit))

	if opts.search != "" {
		query := buildQuery(opts.search)
		opts.f.Logger.Debug(opts.f.Localizer.MustLocalize("kafka.list.log.debug.filteringKafkaList", localize.NewEntry("Search", query)))
		a = a.Search(query)
	}

	// KIOTA

	tokenProvider := RedHatAccessTokenProvider{accessToken: api.GetConfig().AccessToken}

	provider := authentication.NewBaseBearerTokenAuthenticationProvider(tokenProvider)

	adapter, err := http.NewNetHttpRequestAdapter(provider)

	if err != nil {
		fmt.Printf("Error creating request adapter: %v\n", err)
	}

	fmt.Printf("+++ Using Kiota client\n")

	kiotaClient := apisdk.NewApiClient(adapter)

	kiotaResponse, err := kiotaClient.Api().Kafkas_mgmt().V1().Kafkas().Get(opts.f.Context, nil)

	if err != nil {
		return err
	}

	//for i, x := range kiotaResponse.GetItems() {
	//	fmt.Printf("Element %d kafka: %s\n", i, *x.GetName())
	//}

	// end KIOTA

	if len(kiotaResponse.GetItems()) == 0 && opts.outputFormat == "" {
		opts.f.Logger.Info(opts.f.Localizer.MustLocalize("kafka.common.log.info.noKafkaInstances"))
		return nil
	}

	clusterIdMap, err := getClusterIdMapFromKafkas(opts, kiotaResponse.GetItems())
	if err != nil {
		return err
	}

	switch opts.outputFormat {
	case dump.EmptyFormat:
		var rows []kafkaRow
		svcContext, err := opts.f.ServiceContext.Load()
		if err != nil {
			return err
		}

		currCtx, err := contextutil.GetCurrentContext(svcContext, opts.f.Localizer)
		if err != nil {
			return err
		}

		if currCtx.KafkaID != "" {
			rows = mapResponseItemsToRows(opts, kiotaResponse.GetItems(), currCtx.KafkaID, &clusterIdMap)
		} else {
			rows = mapResponseItemsToRows(opts, kiotaResponse.GetItems(), "-", &clusterIdMap)
		}
		dump.Table(opts.f.IOStreams.Out, rows)
		opts.f.Logger.Info("")
	default:
		return dump.Formatted(opts.f.IOStreams.Out, opts.outputFormat, kiotaResponse)
	}
	return nil
}

func mapResponseItemsToRows(opts *options, kafkas []models.KafkaRequestable, selectedId string, clusterIdMap *map[string]*v1.Cluster) []kafkaRow {
	rows := make([]kafkaRow, len(kafkas))

	for i := range kafkas {
		k := kafkas[i]
		name := *k.GetName()
		if *k.GetId() == selectedId {
			name = fmt.Sprintf("%s %s", name, icon.Emoji("✔", "(current)"))
		}

		var openshiftCluster string
		if k.GetClusterId() != nil {
			cluster := (*clusterIdMap)[*k.GetClusterId()]
			openshiftCluster = fmt.Sprintf("%v (%v)", cluster.Name(), cluster.ID())
		} else {
			openshiftCluster = opts.f.Localizer.MustLocalize("kafka.list.output.openshiftCluster.redhat")
		}

		row := kafkaRow{
			ID:               *k.GetId(),
			Name:             name,
			Owner:            *k.GetOwner(),
			Status:           *k.GetStatus(),
			CloudProvider:    *k.GetCloudProvider(),
			Region:           *k.GetRegion(),
			OpenshiftCluster: openshiftCluster,
		}

		rows[i] = row
	}

	return rows
}

func getClusterIdMapFromKafkas(opts *options, kafkas []models.KafkaRequestable) (map[string]*v1.Cluster, error) {
	// map[string]struct{} is used remove duplicated ids from being added to the request
	kafkaClusterIds := make(map[string]struct{})
	for i := 0; i < len(kafkas); i++ {
		kafka := kafkas[i]
		if kafka.GetClusterId() != nil {
			kafkaClusterIds[*kafka.GetClusterId()] = struct{}{}
		}
	}

	idToCluster := make(map[string]*v1.Cluster)

	// if no kafkas have a cluster id assigned then we can skip the call to get
	// the clusters as we dont need their info
	if len(kafkaClusterIds) == 0 {
		return idToCluster, nil
	}

	clusterList, err := clustermgmt.GetClusterListWithSearchParams(opts.f, opts.clusterManagementApiUrl, opts.accessToken, createSearchString(&kafkaClusterIds), int(cmdutil.ConvertPageValueToInt32(build.DefaultPageNumber)), len(kafkaClusterIds))
	if err != nil {
		return nil, err
	}

	for _, cluster := range clusterList.Slice() {
		idToCluster[cluster.ID()] = cluster
	}

	return idToCluster, nil
}

func createSearchString(idSet *map[string]struct{}) string {
	searchString := ""
	index := 0
	for id := range *idSet {
		if index > 0 {
			searchString += " or "
		}
		searchString += fmt.Sprintf("id = '%s'", id)
		index += 1
	}
	return searchString
}

func buildQuery(search string) string {
	queryString := fmt.Sprintf(
		"name like %%%[1]v%% or owner like %%%[1]v%% or cloud_provider like %%%[1]v%% or region like %%%[1]v%% or status like %%%[1]v%%",
		search,
	)

	return queryString
}
