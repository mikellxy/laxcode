package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProviderDeepCopy(t *testing.T) {
	original := Provider{
		ProviderName: "provider", AuthType: "api_key", OpenaiApiKey: "key",
		OpenaiBaseUrl: "https://example.com/v1",
		ModelList: []Model{
			{ModelName: "alias", UpstreamModel: "actual", ReasoningEffort: "medium", Limit: &ModelLimit{Context: 1000, Output: 100}},
			{ModelName: "no-limit"},
		},
	}
	cloned := original.DeepCopy()
	if !reflect.DeepEqual(cloned, original) {
		t.Fatal("deep copy changed provider values")
	}
	if &cloned.ModelList[0] == &original.ModelList[0] || cloned.ModelList[0].Limit == original.ModelList[0].Limit {
		t.Fatal("deep copy shares model storage")
	}
	cloned.ProviderName = "changed"
	cloned.ModelList[0].ModelName = "changed"
	cloned.ModelList[0].Limit.Context = 2000
	cloned.ModelList[0].Limit.Output = 200
	cloned.ModelList[1].ModelName = "changed"
	if original.ProviderName != "provider" || original.ModelList[0].ModelName != "alias" ||
		*original.ModelList[0].Limit != (ModelLimit{Context: 1000, Output: 100}) || original.ModelList[1].ModelName != "no-limit" {
		t.Fatal("copy mutations changed the original provider")
	}
	original.ModelList[0].Limit.Context = 3000
	if cloned.ModelList[0].Limit.Context != 2000 {
		t.Fatal("original mutation changed the copy")
	}

	for _, models := range [][]Model{nil, {}} {
		provider := Provider{ModelList: models}
		if !reflect.DeepEqual(provider.DeepCopy(), provider) {
			t.Fatal("deep copy changed a nil or empty model list")
		}
	}
}

func swapConfigGlobals(t *testing.T) {
	t.Helper()
	previous := Current
	Current = Conf{}
	t.Cleanup(func() { Current = previous })
}

func writeSettings(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".laxcode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func parseTestConfig() error {
	conf, err := Load(os.Getenv("HOME"), nil)
	if err == nil {
		Current = conf
	}
	return err
}

func TestParseCLIRejectsRemovedFlagsAndPositionalArguments(t *testing.T) {
	for _, arg := range []string{"-sse", "-mode=code", "-kb=x", "-session=x", "-token-budget=1000", "unexpected"} {
		if _, err := Load(t.TempDir(), []string{arg}); err == nil {
			t.Fatalf("accepted %q", arg)
		}
	}
}

func TestLoadIgnoresRetiredModelEnvironmentAndGlobalLimits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"model":"p:chat","compaction_model":"p:summary","openai_context_window":1,"openai_max_output_tokens":999999,"compaction_openai_context_window":-1,"compaction_openai_max_output_tokens":999999,"provider_list":[{"provider_name":"p","openai_api_key":"file-key","openai_base_url":"https://file.example/v1","model_list":[{"model_name":"chat"},{"model_name":"summary","limit":{"context":128000,"output":8192}}]}]}`)
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL_NAME", "OPENAI_CONTEXT_WINDOW", "OPENAI_MAX_OUTPUT_TOKENS", "OPENAI_COMPACTION_API_KEY", "OPENAI_COMPACTION_BASE_URL", "OPENAI_COMPACTION_MODEL_NAME", "COMPACTION_OPENAI_CONTEXT_WINDOW", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "COMPACTION_MODEL", "LLM_ROUTER_ADDR"} {
		t.Setenv(key, "ignored-invalid-value")
	}
	conf, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Model != "p:chat" || conf.CompactionModel != "p:summary" || len(conf.ProviderList) != 1 || *conf.ProviderList[0].ModelList[0].Limit != DefaultModelLimit() || conf.ProviderList[0].ModelList[1].Limit.Context != 128000 {
		t.Fatalf("retired settings were applied: %+v", conf)
	}
}

func TestLoadRejectsInvalidModelsAndJSONStructure(t *testing.T) {
	for _, content := range []string{
		`null`, `[]`, `{"model":`,
		`{"provider_list":[{"provider_name":"p","model_list":"invalid"}]}`,
		`{"provider_list":[{"provider_name":"p","model_list":[{"model_name":"m","limit":{}}]}]}`,
		`{"provider_list":[{"provider_name":"p","model_list":[{"model_name":"m","limit":{"context":1000}}]}]}`,
		`{"provider_list":[{"provider_name":"p","model_list":[{"model_name":"m","limit":{"context":1000,"output":1000}}]}]}`,
	} {
		home := t.TempDir()
		writeSettings(t, home, content)
		if _, err := Load(home, nil); err == nil {
			t.Fatalf("accepted invalid settings: %s", content)
		}
	}
}

func TestLoadParsesTypedProvidersAndDefaultsIndependently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"MODEL":"CaseSensitive:alias","PROVIDER_LIST":[{"provider_name":"CaseSensitive","openai_api_key":"key","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"alias","upstream_model":"actual","reasoning_effort":"","limit":{"context":100000,"output":8000}},{"model_name":"plain"},{"model_name":"other"}]}]}`)
	conf, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	models := conf.ProviderList[0].ModelList
	if conf.Model != "CaseSensitive:alias" || models[0].UpstreamModel != "actual" || models[0].Limit.Context != 100000 {
		t.Fatalf("typed fields lost: %+v", conf)
	}
	models[1].Limit.Context = 1
	if models[2].Limit.Context != DefaultContextWindow {
		t.Fatal("default model limits share a pointer")
	}
}

func TestLoadAppliesEnvFileWithoutModelOverrides(t *testing.T) {
	t.Setenv("LAXCODE_ENVCONF_PROBE", "inherited")
	t.Setenv("OPENAI_MODEL_NAME", "inherited-model")
	home := writeEnvConf(t, "LAXCODE_ENVCONF_PROBE=from-file\nOPENAI_MODEL_NAME=ignored-model\n")
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"model":"p:chat","provider_list":[{"provider_name":"p","openai_api_key":"key","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"chat"}]}]}`)
	conf, err := Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("LAXCODE_ENVCONF_PROBE") != "from-file" || os.Getenv("OPENAI_MODEL_NAME") != "ignored-model" || conf.Model != "p:chat" || len(conf.ProviderList) != 1 {
		t.Fatal("env.conf injection or file model selection changed")
	}
}

func TestRetiredMemorySettingsDoNotBlockStartup(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"embedding_model":"missing:model","embedding_vec_dim":-1,"user_memory_executable":"missing"}`)
	t.Setenv("OPENAI_EMBEDDING_API_KEY", "unused")
	if err := parseTestConfig(); err != nil {
		t.Fatalf("retired configuration blocked Web startup: %v", err)
	}
}

func TestSettingsDoNotApplyUserEnvironment(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{}`)
	if err := os.WriteFile(filepath.Join(home, ".laxcode", "env.conf"), []byte("LAXCODE_CLOUD_ENV_PROBE=user-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAXCODE_CLOUD_ENV_PROBE", "server-value")
	conf, err := LoadSettings(home, []string{"-addr=127.0.0.1:0"})
	if err != nil || conf.Addr != "127.0.0.1:0" {
		t.Fatalf("cloud flags: %+v %v", conf, err)
	}
	if os.Getenv("LAXCODE_CLOUD_ENV_PROBE") != "server-value" {
		t.Fatal("user env changed process environment")
	}
}

func TestConfCopyDirFlag(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(t.TempDir(), "configuration source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{source, relative} {
		conf, err := LoadSettings(home, []string{"-conf_copy_dir=" + input})
		if err != nil || conf.ConfCopyDir != source {
			t.Fatalf("source %q: dir=%q err=%v", input, conf.ConfCopyDir, err)
		}
	}
	conf, err := LoadSettings(home, nil)
	if err != nil || conf.ConfCopyDir != "" {
		t.Fatalf("default copy dir: %+v %v", conf, err)
	}
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{file, filepath.Join(home, "missing")} {
		if _, err := LoadSettings(home, []string{"-conf_copy_dir=" + invalid}); err == nil {
			t.Fatalf("accepted invalid source %q", invalid)
		}
	}
}

func TestCloudBashImageFlag(t *testing.T) {
	for _, image := range []string{"laxcode-cloud-bash:local", "registry.example/cloud-bash:v1"} {
		conf, err := LoadSettings(t.TempDir(), []string{"-cloud_bash_image=" + image})
		if err != nil || conf.CloudBashImage != image {
			t.Fatalf("image=%s conf=%+v err=%v", image, conf, err)
		}
	}
	for _, image := range []string{"", "--privileged", "bad image"} {
		if _, err := LoadSettings(t.TempDir(), []string{"-cloud_bash_image=" + image}); err == nil {
			t.Fatalf("accepted %q", image)
		}
	}
}
