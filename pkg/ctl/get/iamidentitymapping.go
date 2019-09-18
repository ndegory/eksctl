package get

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"

	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/authconfigmap"
	"github.com/weaveworks/eksctl/pkg/ctl/cmdutils"
	"github.com/weaveworks/eksctl/pkg/iam"
	"github.com/weaveworks/eksctl/pkg/printers"
)

func getIAMIdentityMappingCmd(cmd *cmdutils.Cmd) {
	cfg := api.NewClusterConfig()
	cmd.ClusterConfig = cfg

	var arn iam.ARN

	params := &getCmdParams{}

	cmd.SetDescription("iamidentitymapping", "Get IAM identity mapping(s)", "")

	cmd.SetRunFunc(func() error {
		return doGetIAMIdentityMapping(cmd, params, arn)
	})

	cmd.FlagSetGroup.InFlagSet("General", func(fs *pflag.FlagSet) {
		cmdutils.AddIAMIdentityMappingARNFlags(fs, cmd, arn)
		cmdutils.AddNameFlag(fs, cfg.Metadata)
		cmdutils.AddRegionFlag(fs, cmd.ProviderConfig)
		cmdutils.AddCommonFlagsForGetCmd(fs, &params.chunkSize, &params.output)
		cmdutils.AddConfigFileFlag(fs, &cmd.ClusterConfigFile)
		cmdutils.AddTimeoutFlag(fs, &cmd.ProviderConfig.WaitTimeout)
	})

	cmdutils.AddCommonFlagsForAWS(cmd.FlagSetGroup, cmd.ProviderConfig, false)
}

func doGetIAMIdentityMapping(cmd *cmdutils.Cmd, params *getCmdParams, arn iam.ARN) error {
	if err := cmdutils.NewMetadataLoader(cmd).Load(); err != nil {
		return err
	}

	cfg := cmd.ClusterConfig

	ctl, err := cmd.NewCtl()
	if err != nil {
		return err
	}

	if err := ctl.CheckAuth(); err != nil {
		return err
	}

	if cfg.Metadata.Name == "" {
		return cmdutils.ErrMustBeSet("--name")
	}

	if ok, err := ctl.CanOperate(cfg); !ok {
		return err
	}
	clientSet, err := ctl.NewStdClientSet(cfg)
	if err != nil {
		return err
	}
	acm, err := authconfigmap.NewFromClientSet(clientSet)
	if err != nil {
		return err
	}
	identities, err := acm.Identities()
	if err != nil {
		return err
	}

	if arn.Resource != "" {
		_identities := []iam.Identity{}

		for _, identity := range identities {
			_arn, err := identity.ARN()
			if err != nil {
				return err
			}
			if _arn.String() == arn.String() {
				_identities = append(_identities, identity)
			}
		}

		identities = _identities
		// If a filter was given, we error if none was found
		if len(identities) == 0 {
			return fmt.Errorf("no iamidentitymapping with arn %q found", arn)
		}
	}

	printer, err := printers.NewPrinter(params.output)
	if err != nil {
		return err
	}
	if params.output == "table" {
		addIAMIdentityMappingTableColumns(printer.(*printers.TablePrinter))
	}

	if err := printer.PrintObjWithKind("iamidentitymappings", identities, os.Stdout); err != nil {
		return err
	}

	return nil
}

func addIAMIdentityMappingTableColumns(printer *printers.TablePrinter) {
	printer.AddColumn("ARN", func(r iam.Identity) string {
		arn, err := r.ARN()
		if err == nil {
			return arn.String()
		}
		return ""
	})
	printer.AddColumn("USERNAME", func(r iam.Identity) string {
		if r.Username != nil {
			return *r.Username
		}
		return ""
	})
	printer.AddColumn("GROUPS", func(r iam.Identity) string {
		if r.Groups != nil {
			return strings.Join(r.Groups, ",")
		}
		return ""
	})
}
