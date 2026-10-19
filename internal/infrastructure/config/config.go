package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/spf13/viper"
)

// Conf is the merged startup configuration. Runtime model selections belong to ai_models.
type Conf struct {
	Model           string
	CompactionModel string
	ProviderList    []Provider
	Addr            string
	Plan            bool
}

type Provider struct {
	ProviderName string `json:"provider_name" mapstructure:"provider_name"`
	// AuthType is retained only to reject or skip legacy subscription entries.
	AuthType      string  `json:"auth_type,omitempty" mapstructure:"auth_type"`
	OpenaiApiKey  string  `json:"openai_api_key,omitempty" mapstructure:"openai_api_key"`
	OpenaiBaseUrl string  `json:"openai_base_url" mapstructure:"openai_base_url"`
	ModelList     []Model `json:"model_list" mapstructure:"model_list"`
}

// DeepCopy returns a provider with an independent model list and model limits.
func (p Provider) DeepCopy() Provider {
	cloned := p
	if p.ModelList != nil {
		cloned.ModelList = make([]Model, len(p.ModelList))
		copy(cloned.ModelList, p.ModelList)
		for i := range cloned.ModelList {
			if limit := cloned.ModelList[i].Limit; limit != nil {
				limitCopy := *limit
				cloned.ModelList[i].Limit = &limitCopy
			}
		}
	}
	return cloned
}

type Model struct {
	ModelName       string      `json:"model_name" mapstructure:"model_name"`
	UpstreamModel   string      `json:"upstream_model,omitempty" mapstructure:"upstream_model"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty" mapstructure:"reasoning_effort"`
	Limit           *ModelLimit `json:"limit,omitempty" mapstructure:"limit"`
}

type ModelLimit struct {
	Context int `json:"context" mapstructure:"context"`
	Output  int `json:"output" mapstructure:"output"`
}

type fileConf struct {
	Model           string     `json:"model" mapstructure:"model"`
	CompactionModel string     `json:"compaction_model,omitempty" mapstructure:"compaction_model"`
	ProviderList    []Provider `json:"provider_list" mapstructure:"provider_list"`
}

type cliConf struct {
	Addr string `mapstructure:"addr"`
	Plan bool   `mapstructure:"plan"`
}

const (
	DefaultSSEAddr         = "127.0.0.1:8080"
	DefaultContextWindow   = 200_000
	DefaultMaxOutputTokens = 16_384
)

var Current Conf

func DefaultModelLimit() ModelLimit {
	return ModelLimit{Context: DefaultContextWindow, Output: DefaultMaxOutputTokens}
}

// Parse loads settings and CLI arguments before any outbound requests.
func Parse() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	conf, err := Load(homeDir, os.Args[1:])
	if err != nil {
		return err
	}
	Current = conf
	return nil
}

func Load(homeDir string, args []string) (Conf, error) {
	return LoadSettings(homeDir, args)
}

// LoadSettings reads the platform catalog and resolves OpenShell environment references.
func LoadSettings(homeDir string, args []string) (Conf, error) {
	return LoadSettingsFromDir(layout.Root(homeDir), args)
}

func LoadSettingsFromDir(configDir string, args []string) (Conf, error) {
	cli, err := parseCLI(args)
	if err != nil {
		return Conf{}, err
	}
	file, err := parseFile(configDir)
	if err != nil {
		return Conf{}, err
	}
	conf, err := merge(file, cli)
	if err != nil {
		return Conf{}, err
	}
	if err := LoadProviderAPIKeys(conf.ProviderList); err != nil {
		return Conf{}, err
	}
	return conf, nil
}

// LoadCatalogFromDir reads deployment metadata without resolving inference credentials.
func LoadCatalogFromDir(dir string) (Conf, error) {
	file, err := parseFile(dir)
	if err != nil {
		return Conf{}, err
	}
	return merge(file, cliConf{})
}

func parseFile(configDir string) (fileConf, error) {
	data, err := os.ReadFile(layout.SettingsFile(configDir))
	if errors.Is(err, os.ErrNotExist) {
		return fileConf{}, nil
	}
	if err != nil {
		return fileConf{}, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return fileConf{}, errors.New("settings must be a JSON object")
	}
	reader := viper.New()
	reader.SetConfigType("json")
	if err := reader.ReadConfig(bytes.NewReader(data)); err != nil {
		return fileConf{}, err
	}
	var file fileConf
	if err := reader.Unmarshal(&file); err != nil {
		return fileConf{}, err
	}
	return file, nil
}

func parseCLI(args []string) (cliConf, error) {
	var cli cliConf
	flags := flag.NewFlagSet("laxcode", flag.ContinueOnError)
	// The caller reports parse errors; avoid printing the same diagnostic twice.
	flags.SetOutput(io.Discard)
	flags.Usage = func() {
		flags.SetOutput(os.Stderr)
		fmt.Fprintln(os.Stderr, "Usage of laxcode:")
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	flags.StringVar(&cli.Addr, "addr", DefaultSSEAddr, "Web backend listen address")
	flags.BoolVar(&cli.Plan, "plan", false, "enable plan mode")
	if err := flags.Parse(args); err != nil {
		return cliConf{}, err
	}
	if flags.NArg() != 0 {
		return cliConf{}, fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	return cli, nil
}

func merge(file fileConf, cli cliConf) (Conf, error) {
	conf := Conf{Addr: DefaultSSEAddr}
	conf.Model, conf.CompactionModel = file.Model, file.CompactionModel
	conf.ProviderList = file.ProviderList
	conf.Addr, conf.Plan = cli.Addr, cli.Plan
	for i := range conf.ProviderList {
		for j := range conf.ProviderList[i].ModelList {
			model := &conf.ProviderList[i].ModelList[j]
			if model.Limit == nil {
				limit := DefaultModelLimit()
				model.Limit = &limit
			}
			if model.Limit.Context <= 0 || model.Limit.Output <= 0 || model.Limit.Output >= model.Limit.Context {
				return Conf{}, fmt.Errorf("invalid limit for %s:%s: context and output must be positive and output smaller than context", conf.ProviderList[i].ProviderName, model.ModelName)
			}
		}
	}
	return conf, nil
}
