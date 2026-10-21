package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentmodels"
	"github.com/subgentic/coral/internal/settings"
)

// The settings registry (#4432, Security's #4431 SHOULD 3): every key PUT /api/settings knows, who may write it and
// what its value may be. A key it doesn't list, or one in another case, is refused, so a typo or a key a newer
// dashboard invented can't be stored where nothing reads it. The whole route is never reachable through Coral Hub
// (the tunnel's table) and refuses agent tokens, so there is no Hub-only class here: every generic key is the local
// Operator's.

// settingClass says who writes a setting.
type settingClass int

const (
	settingGeneric  settingClass = iota // PUT /api/settings
	settingOwnRoute                     // only its own route; the generic PUT refuses it (see PutSettings' messages)
	settingCoral                        // Coral's own bookkeeping; no client writes it
)

// Size caps (INFO 3): 256 KiB for a value, 2 MiB for the JSON list settings.
const (
	settingMaxLen     = 256 << 10
	settingListMaxLen = 2 << 20
)

// settingSpec is one key's entry. check, when set, validates a generic key's value (a string, a bool, or a whole
// number as a string by then) and returns what is stored; nil leaves it as it is (PutSettings' own validators, which
// run after the registry, check those keys).
type settingSpec struct {
	class  settingClass
	check  func(v any) (any, error)
	maxLen int
}

func generic(check func(v any) (any, error)) settingSpec {
	return settingSpec{class: settingGeneric, check: check}
}

// boolSetting is true or false: a JSON boolean, or the words config.Truthy reads (stored as sent).
func boolSetting(v any) (any, error) {
	if _, ok := validBoolSetting(v); !ok {
		return nil, errors.New("must be true or false")
	}
	return v, nil
}

// textSetting is any text (the size cap and the NUL check apply to every key).
func textSetting(v any) (any, error) {
	if _, ok := v.(string); !ok {
		return nil, errors.New("must be text")
	}
	return v, nil
}

// enumSetting is one of values, or empty for the default.
func enumSetting(values ...string) func(any) (any, error) {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if ok && s == "" {
			return v, nil
		}
		for _, want := range values {
			if ok && s == want {
				return v, nil
			}
		}
		return nil, fmt.Errorf("must be one of %s", strings.Join(values, ", "))
	}
}

// intSetting is a whole number from lo to hi, or empty for the default; clamp brings one outside the range into it
// instead of refusing it.
func intSetting(lo, hi int64, clamp bool) func(any) (any, error) {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		}
		if strings.TrimSpace(s) == "" {
			return "", nil
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		switch {
		case err != nil:
			return nil, fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		case clamp:
			n = max(lo, min(hi, n))
		case n < lo || n > hi:
			return nil, fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		}
		return strconv.FormatInt(n, 10), nil
	}
}

// listSetting is a JSON list kept as text (the dashboard's saved personas, templates, macros and list orders).
func listSetting(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, errors.New("must be a JSON list, as text")
	}
	if strings.TrimSpace(s) == "" {
		return v, nil
	}
	var list []json.RawMessage
	if json.Unmarshal([]byte(s), &list) != nil {
		return nil, errors.New("must be a JSON list, as text")
	}
	return v, nil
}

// agentTypes are the agent types a default or a per-type model may name.
var agentTypes = settings.AgentTypes

// settingRegistry is derived from the typed registry in internal/settings (#5147): the kind, the range and the values of
// a key are declared there once, for the readers and for this check. A key whose value has rules beyond its type
// (PutSettings' own validators, which run after this) has no check here: whole numbers with no declared range, and
// settings of kind opaque.
var settingRegistry = deriveSettingRegistry()

func deriveSettingRegistry() map[string]settingSpec {
	out := map[string]settingSpec{}
	for _, sp := range settings.All() {
		out[sp.Key] = settingSpecFor(sp)
	}
	return out
}

func settingSpecFor(sp settings.Spec) settingSpec {
	switch sp.Class {
	case settings.ClassOwnRoute:
		return settingSpec{class: settingOwnRoute}
	case settings.ClassCoral:
		return settingSpec{class: settingCoral}
	}
	out := settingSpec{class: settingGeneric, maxLen: sp.MaxLen}
	if sp.OwnCheck {
		return out
	}
	switch sp.Kind {
	case settings.KindBool:
		out.check = boolSetting
	case settings.KindText:
		out.check = textSetting
	case settings.KindEnum:
		out.check = enumSetting(sp.Values...)
	case settings.KindList:
		out.check = listSetting
	case settings.KindInt:
		if sp.Min != 0 || sp.Max != 0 {
			out.check = intSetting(sp.Min, sp.Max, sp.WriteClamp)
		}
	}
	return out
}

// lookupSetting is key's entry: an exact key of the registry, or cli_path_<type> and default_model_<type> for a
// known type.
func lookupSetting(key string) (settingSpec, bool) {
	if spec, ok := settingRegistry[key]; ok {
		return spec, true
	}
	if t, ok := agent.IsCLIPathSetting(key); ok && key == strings.ToLower(key) {
		return settingSpec{class: settingGeneric}, agent.KnownCLIAgentType(t) // PutSettings checks the path
	}
	if t, ok := strings.CutPrefix(key, "default_model_"); ok {
		for _, known := range agentTypes {
			if t == known {
				return generic(textSetting), true
			}
		}
	}
	if t, ok := strings.CutPrefix(key, "default_thinking_"); ok {
		for _, known := range agentTypes {
			if t == known {
				return generic(thinkingSetting(t)), true
			}
		}
	}
	return settingSpec{}, false
}

// checkSettingKeys applies the registry to a PUT's body: a key it doesn't know, or Coral's own, is refused; a generic
// key's value is checked (no NUL, at most its size) and normalised. PutSettings refuses the own-route keys with the
// route to use before this runs; one that gets here is refused too.
func checkSettingKeys(body map[string]any) error {
	for k, v := range body {
		spec, ok := lookupSetting(k)
		if !ok {
			if _, isCLIPath := agent.IsCLIPathSetting(k); isCLIPath {
				return errors.New("unknown agent type in " + k)
			}
			return fmt.Errorf("%q is not a Coral setting", k)
		}
		switch spec.class {
		case settingCoral:
			return fmt.Errorf("%s is set by Coral", k)
		case settingOwnRoute:
			return fmt.Errorf("%s is set with its own route, not PUT /api/settings", k)
		}
		if s, isString := v.(string); isString {
			limit := spec.maxLen
			if limit == 0 {
				limit = settingMaxLen
			}
			if len(s) > limit {
				return fmt.Errorf("%s is too long (at most %d KiB)", k, limit>>10)
			}
			if strings.ContainsRune(s, 0) {
				return fmt.Errorf("%s must not contain a NUL character", k)
			}
		}
		if spec.check != nil {
			out, err := spec.check(v)
			if err != nil {
				return fmt.Errorf("%s %v", k, err)
			}
			body[k] = out
		}
	}
	return nil
}

// thinkingSetting checks a default_thinking_<type> value (#4969): "" (the CLI's own default) or one of the levels Coral passes to that
// type's CLI.
func thinkingSetting(agentType string) func(v any) (any, error) {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		s = strings.TrimSpace(s)
		if !agentmodels.ValidThinking(agentType, s) {
			return nil, errors.New(errInvalidThinking(agentType))
		}
		return s, nil
	}
}
