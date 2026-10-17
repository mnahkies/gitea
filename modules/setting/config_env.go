// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"gitea.dev/modules/log"
)

const (
	EnvConfigKeyPrefixGitea = "GITEA__"
	EnvConfigKeySuffixFile  = "__FILE"
)

const escapeRegexpString = "_0[xX](([0-9a-fA-F][0-9a-fA-F])+)_"

var escapeRegex = regexp.MustCompile(escapeRegexpString)

var (
	dynEnvConfigVarsMu sync.Mutex
	dynEnvConfigVars   = map[string]string{} // env name -> raw env value (may be a file path for the "__FILE" suffix), for re-injecting into a restarted process
)

func CollectEnvConfigKeys() (keys []string) {
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, EnvConfigKeyPrefixGitea) {
			k, _, _ := strings.Cut(env, "=")
			keys = append(keys, k)
		}
	}
	return keys
}

func ClearEnvConfigKeys() {
	for _, k := range CollectEnvConfigKeys() {
		_ = os.Unsetenv(k)
	}
}

// decodeEnvSectionKey will decode a portable string encoded Section__Key pair
// Portable strings are considered to be of the form [A-Z0-9_]*
// We will encode a disallowed value as the UTF8 byte string preceded by _0X and
// followed by _. E.g. _0X2C_ for a '-' and _0X2E_ for '.'
// Section and Key are separated by a plain '__'.
// The entire section can be encoded as a UTF8 byte string
func decodeEnvSectionKey(encoded string) (ok bool, section, key string) {
	inKey := false
	last := 0
	escapeStringIndices := escapeRegex.FindAllStringIndex(encoded, -1)
	for _, unescapeIdx := range escapeStringIndices {
		preceding := encoded[last:unescapeIdx[0]]
		if !inKey {
			if before, after, cutOk := strings.Cut(preceding, "__"); cutOk {
				section += before
				inKey = true
				key += after
			} else {
				section += preceding
			}
		} else {
			key += preceding
		}
		toDecode := encoded[unescapeIdx[0]+3 : unescapeIdx[1]-1]
		decodedBytes := make([]byte, len(toDecode)/2)
		for i := 0; i < len(toDecode)/2; i++ {
			// Can ignore error here as we know these should be hexadecimal from the regexp
			byteInt, _ := strconv.ParseInt(toDecode[2*i:2*i+2], 16, 8)
			decodedBytes[i] = byte(byteInt)
		}
		if inKey {
			key += string(decodedBytes)
		} else {
			section += string(decodedBytes)
		}
		last = unescapeIdx[1]
	}
	remaining := encoded[last:]
	if !inKey {
		if before, after, cutOk := strings.Cut(remaining, "__"); cutOk {
			section += before
			key += after
		} else {
			section += remaining
		}
	} else {
		key += remaining
	}
	section = strings.ToLower(section)
	ok = key != ""
	if !ok {
		section = ""
		key = ""
	}
	return ok, section, key
}

// decodeEnvironmentKey decode the environment key to section and key
// The environment key is in the form of GITEA__SECTION__KEY or GITEA__SECTION__KEY__FILE
func decodeEnvironmentKey(prefixGitea, suffixFile, envKey string) (ok bool, section, key string, useFileValue bool) {
	if !strings.HasPrefix(envKey, prefixGitea) {
		return false, "", "", false
	}
	if strings.HasSuffix(envKey, suffixFile) {
		useFileValue = true
		envKey = envKey[:len(envKey)-len(suffixFile)]
	}
	ok, section, key = decodeEnvSectionKey(envKey[len(prefixGitea):])
	return ok, section, key, useFileValue
}

func EnvironmentToConfig(cfg ConfigProvider, envs []string) (changed bool) {
	for _, kv := range envs {
		before, after, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}

		// parse the environment variable to config section name and key name
		envKey := before
		envValue := after
		ok, sectionName, keyName, useFileValue := decodeEnvironmentKey(EnvConfigKeyPrefixGitea, EnvConfigKeySuffixFile, envKey)
		if !ok {
			continue
		}
		// remember this environment variable, it must be re-injected into a restarted
		// process, since the values it provides are not persisted into the config file
		recordDynEnvConfigVar(envKey, envValue)

		// use environment value as config value, or read the file content as value if the key indicates a file
		keyValue := envValue
		if useFileValue {
			fileContent, err := os.ReadFile(envValue)
			if err != nil {
				log.Error("Error reading file for %s : %v", envKey, envValue, err)
				continue
			}
			if bytes.HasSuffix(fileContent, []byte("\r\n")) {
				fileContent = fileContent[:len(fileContent)-2]
			} else if bytes.HasSuffix(fileContent, []byte("\n")) {
				fileContent = fileContent[:len(fileContent)-1]
			}
			keyValue = string(fileContent)
		}

		// try to set the config value if necessary
		section, err := cfg.GetSection(sectionName)
		if err != nil {
			section, err = cfg.NewSection(sectionName)
			if err != nil {
				log.Error("Error creating section: %s : %v", sectionName, err)
				continue
			}
		}
		key := ConfigSectionKey(section, keyName)
		created := false
		if key == nil {
			created = true
			changed = true
			key, err = section.NewKey(keyName, keyValue)
			if err != nil {
				log.Error("Error creating key: %s in section: %s with value: %s : %v", keyName, sectionName, keyValue, err)
				continue
			}
		}
		oldValue := key.Value()
		if !changed && oldValue != keyValue {
			changed = true
		}
		key.SetValue(keyValue)
		// remember that this key is provided by an environment variable, so its value
		// stays in memory only and is never persisted into the config file
		recordEnvConfigKeyIntoProvider(cfg, sectionName, keyName, !created, oldValue)
	}
	return changed
}

// recordEnvConfigKeyIntoProvider records that a config key is provided by an environment
// variable. Fallback reflects the config-file value before the environment value was
// applied: when the config provider is saved, the key is either omitted (created by an
// environment variable: hasFallback=false) or reset to its config-file value
// (hasFallback=true). The first registration wins, since later applications run on
// already-overlaid configs where the "fallback" value would just be the previous
// environment value.
func recordEnvConfigKeyIntoProvider(cfg ConfigProvider, sectionName, keyName string, hasFallback bool, fallback string) {
	p, ok := cfg.(*iniConfigProvider)
	if !ok {
		return
	}
	ck := envConfigKey{sectionName, keyName}
	if _, exist := p.envConfigKeys[ck]; !exist {
		p.envConfigKeys[ck] = envConfigValue{Fallback: fallback, HasFallback: hasFallback}
	}
}

func recordDynEnvConfigVar(envKey, envValue string) {
	dynEnvConfigVarsMu.Lock()
	defer dynEnvConfigVarsMu.Unlock()
	dynEnvConfigVars[envKey] = envValue
}

// DynEnvConfigVariables returns the environment variables which provide config values,
// as "NAME=VALUE" pairs, to be re-injected into a restarted child process, because they
// are unset in the current process (ClearEnvConfigKeys) but the restarted child needs
// them again to overlay its config in memory.
func DynEnvConfigVariables() (envs []string) {
	dynEnvConfigVarsMu.Lock()
	defer dynEnvConfigVarsMu.Unlock()
	for k, v := range dynEnvConfigVars {
		envs = append(envs, k+"="+v)
	}
	return envs
}

func UnsetUnnecessaryEnvVars() {
	// Ideally Gitea should only accept the environment variables which it clearly knows instead of unsetting the ones it doesn't want,
	// but the ideal behavior would be a breaking change, and it seems not bringing enough benefits to end users.
	// So at the moment we just keep "unsetting the unnecessary environment variables".

	// HOME is managed by Gitea, Gitea's git should use "HOME/.gitconfig".
	// But git would try "XDG_CONFIG_HOME/git/config" first if "HOME/.gitconfig" does not exist,
	// then our git.InitFull would still write to "XDG_CONFIG_HOME/git/config" if XDG_CONFIG_HOME is set.
	_ = os.Unsetenv("XDG_CONFIG_HOME")
}
