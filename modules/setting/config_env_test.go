// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDecodeEnvSectionKey(t *testing.T) {
	ok, section, key := decodeEnvSectionKey("SEC__KEY")
	assert.True(t, ok)
	assert.Equal(t, "sec", section)
	assert.Equal(t, "KEY", key)

	ok, section, key = decodeEnvSectionKey("sec__key")
	assert.True(t, ok)
	assert.Equal(t, "sec", section)
	assert.Equal(t, "key", key)

	ok, section, key = decodeEnvSectionKey("LOG_0x2E_CONSOLE__STDERR")
	assert.True(t, ok)
	assert.Equal(t, "log.console", section)
	assert.Equal(t, "STDERR", key)

	ok, section, key = decodeEnvSectionKey("SEC")
	assert.False(t, ok)
	assert.Empty(t, section)
	assert.Empty(t, key)
}

func TestDecodeEnvironmentKey(t *testing.T) {
	prefix := "GITEA__"
	suffix := "__FILE"

	ok, section, key, file := decodeEnvironmentKey(prefix, suffix, "SEC__KEY")
	assert.False(t, ok)
	assert.Empty(t, section)
	assert.Empty(t, key)
	assert.False(t, file)

	ok, section, key, file = decodeEnvironmentKey(prefix, suffix, "GITEA__SEC")
	assert.False(t, ok)
	assert.Empty(t, section)
	assert.Empty(t, key)
	assert.False(t, file)

	ok, section, key, file = decodeEnvironmentKey(prefix, suffix, "GITEA____KEY")
	assert.True(t, ok)
	assert.Empty(t, section)
	assert.Equal(t, "KEY", key)
	assert.False(t, file)

	ok, section, key, file = decodeEnvironmentKey(prefix, suffix, "GITEA__SEC__KEY")
	assert.True(t, ok)
	assert.Equal(t, "sec", section)
	assert.Equal(t, "KEY", key)
	assert.False(t, file)

	// with "__FILE" suffix, it doesn't support to write "[sec].FILE" to config (no such key FILE is used in Gitea)
	// but it could be fixed in the future by adding a new suffix like "__VALUE" (no such key VALUE is used in Gitea either)
	ok, section, key, file = decodeEnvironmentKey(prefix, suffix, "GITEA__SEC__FILE")
	assert.False(t, ok)
	assert.Empty(t, section)
	assert.Empty(t, key)
	assert.True(t, file)

	ok, section, key, file = decodeEnvironmentKey(prefix, suffix, "GITEA__SEC__KEY__FILE")
	assert.True(t, ok)
	assert.Equal(t, "sec", section)
	assert.Equal(t, "KEY", key)
	assert.True(t, file)

	ok, _, _, _ = decodeEnvironmentKey("PREFIX__", "", "PREFIX__SEC__KEY")
	assert.True(t, ok)
}

func TestEnvironmentToConfig(t *testing.T) {
	cfg, _ := NewConfigProviderFromData("")

	changed := EnvironmentToConfig(cfg, nil)
	assert.False(t, changed)

	cfg, err := NewConfigProviderFromData(`
[sec]
key = old
`)
	assert.NoError(t, err)

	changed = EnvironmentToConfig(cfg, []string{"GITEA__sec__key=new"})
	assert.True(t, changed)
	assert.Equal(t, "new", cfg.Section("sec").Key("key").String())

	changed = EnvironmentToConfig(cfg, []string{"GITEA__sec__key=new"})
	assert.False(t, changed)

	tmpFile := t.TempDir() + "/the-file"
	_ = os.WriteFile(tmpFile, []byte("value-from-file"), 0o644)
	changed = EnvironmentToConfig(cfg, []string{"GITEA__sec__key__FILE=" + tmpFile})
	assert.True(t, changed)
	assert.Equal(t, "value-from-file", cfg.Section("sec").Key("key").String())

	cfg, _ = NewConfigProviderFromData("")
	_ = os.WriteFile(tmpFile, []byte("value-from-file\n"), 0o644)
	EnvironmentToConfig(cfg, []string{"GITEA__sec__key__FILE=" + tmpFile})
	assert.Equal(t, "value-from-file", cfg.Section("sec").Key("key").String())

	cfg, _ = NewConfigProviderFromData("")
	_ = os.WriteFile(tmpFile, []byte("value-from-file\r\n"), 0o644)
	EnvironmentToConfig(cfg, []string{"GITEA__sec__key__FILE=" + tmpFile})
	assert.Equal(t, "value-from-file", cfg.Section("sec").Key("key").String())

	cfg, _ = NewConfigProviderFromData("")
	_ = os.WriteFile(tmpFile, []byte("value-from-file\n\n"), 0o644)
	EnvironmentToConfig(cfg, []string{"GITEA__sec__key__FILE=" + tmpFile})
	assert.Equal(t, "value-from-file\n", cfg.Section("sec").Key("key").String())
}

func TestEnvironmentToConfigPersistence(t *testing.T) {
	t.Run("environment-provided keys are not persisted", func(t *testing.T) {
		tmp := t.TempDir()
		cfg, err := NewConfigProviderFromFile(tmp + "/app.ini")
		assert.NoError(t, err)

		fileEnv := tmp + "/the-file"
		assert.NoError(t, os.WriteFile(fileEnv, []byte("value-from-file\n"), 0o644))
		changed := EnvironmentToConfig(cfg, []string{
			"GITEA__sec__existing=env-value",
			"GITEA__sec__created=env-value",
			"GITEA__sec__from_file__FILE=" + fileEnv,
		})
		assert.True(t, changed)
		assert.NoError(t, cfg.SaveTo(tmp+"/saved.ini"))

		// the in-memory config still holds the environment values
		assert.Equal(t, "env-value", cfg.Section("sec").Key("existing").String())
		assert.Equal(t, "env-value", cfg.Section("sec").Key("created").String())
		assert.Equal(t, "value-from-file", cfg.Section("sec").Key("from_file").String())

		// the saved config: "existing" is absent (the config file never had it, the empty
		// section is dropped), "created" and "from_file" are absent
		bs, err := os.ReadFile(tmp + "/saved.ini")
		assert.NoError(t, err)
		assert.Equal(t, "", string(bs))
	})

	t.Run("environment-provided keys kept when explicitly included", func(t *testing.T) {
		tmp := t.TempDir()
		cfg, err := NewConfigProviderFromFile(tmp + "/app.ini")
		assert.NoError(t, err)
		cfg.IncludeEnvConfigKeys()

		changed := EnvironmentToConfig(cfg, []string{"GITEA__sec__created=env-value"})
		assert.True(t, changed)
		assert.NoError(t, cfg.SaveTo(tmp+"/saved.ini"))
		bs, err := os.ReadFile(tmp + "/saved.ini")
		assert.NoError(t, err)
		assert.Equal(t, "[sec]\ncreated = env-value\n", string(bs))
	})

	t.Run("environment-provided values overlay config-file values without changing the file", func(t *testing.T) {
		tmp := t.TempDir()
		assert.NoError(t, os.WriteFile(tmp+"/app.ini", []byte("[sec]\nSECRET_TOKen = from-file\n"), 0o644))
		cfg, err := NewConfigProviderFromFile(tmp + "/app.ini")
		assert.NoError(t, err)

		changed := EnvironmentToConfig(cfg, []string{"GITEA__sec__SECRET_TOKen=from-env"})
		assert.True(t, changed)
		assert.Equal(t, "from-env", cfg.Section("sec").Key("SECRET_TOKen").String())
		assert.NoError(t, cfg.SaveTo(tmp+"/saved.ini"))
		bs, err := os.ReadFile(tmp + "/saved.ini")
		assert.NoError(t, err)
		assert.Equal(t, "[sec]\nSECRET_TOKen = from-file\n", string(bs))
	})
}

func TestEnvironmentToConfigSubSecKey(t *testing.T) {
	// the INI package has a quirk: by default, the keys are inherited.
	// when maintaining the keys, the newly added sub key should not be affected by the parent key.
	cfg, err := NewConfigProviderFromData(`
[sec]
key = some
`)
	assert.NoError(t, err)

	changed := EnvironmentToConfig(cfg, []string{"GITEA__sec_0X2E_sub__key=some"})
	assert.True(t, changed)

	// this test is about the ini sub-section key quirk, not about the env-to-ini persistence
	cfg.IncludeEnvConfigKeys()

	tmpFile := t.TempDir() + "/test-sub-sec-key.ini"
	defer os.Remove(tmpFile)
	err = cfg.SaveTo(tmpFile)
	assert.NoError(t, err)
	bs, err := os.ReadFile(tmpFile)
	assert.NoError(t, err)
	assert.Equal(t, `[sec]
key = some

[sec.sub]
key = some
`, string(bs))
}
