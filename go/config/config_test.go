package config

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	expect "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.skia.org/infra/go/deepequal"
	"go.skia.org/infra/go/testutils"
	"go.skia.org/infra/go/testutils/unittest"
)

type TestInnerConfig struct {
	Name      string
	Frequency Duration
}

type TestConfig struct {
	Delay   Duration
	Count   int
	Percent float64
	Allow   bool
	Sources []string
	Primary TestInnerConfig
	Items   []*TestInnerConfig
	Params  map[string]string
}

func TestDuration(t *testing.T) {
	unittest.SmallTest(t)
	type dummy struct {
		Dur Duration
	}
	orig := dummy{
		Dur: Duration{5 * time.Second},
	}
	enc, err := json.Marshal(&orig)
	require.NoError(t, err)
	expect.Equal(t, `{"Dur":"5s"}`, string(enc))

	parsed := dummy{}
	require.NoError(t, json.Unmarshal(enc, &parsed))
	deepequal.AssertDeepEqual(t, orig, parsed)
}

func TestParseConfigFile(t *testing.T) {
	unittest.MediumTest(t)
	dir, err := testutils.TestDataDir()
	require.NoError(t, err)
	configFile := filepath.Join(dir, "TestParseConfigFile.json5")
	parsed := TestConfig{}
	require.NoError(t, ParseConfigFile(configFile, "", &parsed))
	expected := TestConfig{
		Delay:   Duration{17 * time.Minute},
		Count:   2400,
		Percent: 0.25,
		Allow:   true,
		Sources: []string{"internet", "local", "random"},
		Primary: TestInnerConfig{
			Name:      "run-tests",
			Frequency: Duration{10 * time.Minute},
		},
		Items: []*TestInnerConfig{
			{
				Name:      "cleanup",
				Frequency: Duration{24 * time.Hour},
			},
			nil,
			{},
			{
				Name:      "refresh",
				Frequency: Duration{100 * time.Millisecond},
			},
		},
		Params: map[string]string{
			"os":   "Linux",
			"arch": "amd64",
		},
	}
	deepequal.AssertDeepEqual(t, expected, parsed)
}

func TestParseConfigFileDoesntExist(t *testing.T) {
	unittest.MediumTest(t)
	dir, cleanup := testutils.TempDir(t)
	defer cleanup()
	configFile := filepath.Join(dir, "nonexistent-file.json5")
	parsed := TestConfig{}
	err := ParseConfigFile(configFile, "--main-config", &parsed)
	require.Regexp(t, `Unable to read --main-config file ".*/nonexistent-file.json5":.* no such file or directory`, err.Error())
}

func TestParseConfigFileInvalid(t *testing.T) {
	unittest.MediumTest(t)
	dir, cleanup := testutils.TempDir(t)
	defer cleanup()
	configFile := filepath.Join(dir, "invalid.json5")
	require.NoError(t, ioutil.WriteFile(configFile, []byte("Hi Mom!"), os.ModePerm))
	parsed := TestConfig{}
	err := ParseConfigFile(configFile, "", &parsed)
	require.Regexp(t, `Unable to parse file ".*/invalid.json5": invalid character 'H' looking for beginning of value`, err.Error())
}
