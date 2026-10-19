package settings

import "testing"

func TestBoolWordsAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		key, val string
		set      bool
		want     bool
	}{
		{TelemetryEnabled, "", false, true}, // default on
		{TelemetryEnabled, "False", true, false},
		{TelemetryEnabled, " off ", true, false},
		{TelemetryEnabled, "garbage", true, true}, // unreadable: the default
		{AutoSummarize, "", false, false},         // default off
		{AutoSummarize, "True", true, true},
		{AutoSummarize, "YES", true, true},
		{AutoSummarize, "0", true, false},
		{AutoSummarize, "garbage", true, false},
	} {
		m := map[string]string{}
		if tc.set {
			m[tc.key] = tc.val
		}
		if got := Bool(m, tc.key); got != tc.want {
			t.Errorf("Bool(%s=%q set=%v) = %v, want %v", tc.key, tc.val, tc.set, got, tc.want)
		}
	}
}

func TestIntReadModes(t *testing.T) {
	for _, tc := range []struct {
		key, val  string
		want      int64
		wantState State
	}{
		{UploadsRetentionDays, "", 30, Unset},
		{UploadsRetentionDays, "x", 30, Invalid},
		{UploadsRetentionDays, "400", 365, Valid}, // clamp
		{UploadsRetentionDays, "0", 1, Valid},
		{AutoWakeCap, "99", 8, Invalid}, // out of range reads as the default
		{AutoWakeCap, "3", 3, Valid},
		{KnowledgeTimeoutS, "29", 120, Invalid}, // read floor is 30, write floor is 1
		{KnowledgeTimeoutS, "30", 30, Valid},
		{KnowledgeTimeoutS, "100000", 100000, Valid},
		{GitPollIntervalS, "-5", -5, Valid}, // any number reads as itself
		{AgentLaunchLimit, "-1", 64, Invalid},
		{AgentLaunchLimit, "0", 0, Valid},
		{AgentLaunchLimit, "5000000", 5000000, Valid},
		{FileSearchLimit, "0", 500, Invalid},
		{FileSearchLimit, "250000", 250000, Valid},
	} {
		m := map[string]string{}
		if tc.val != "" {
			m[tc.key] = tc.val
		}
		n, st := IntState(m, tc.key)
		if n != tc.want || st != tc.wantState {
			t.Errorf("IntState(%s=%q) = %d,%v want %d,%v", tc.key, tc.val, n, st, tc.want, tc.wantState)
		}
	}
	if IntOr(map[string]string{}, GitPollIntervalS, 7) != 7 || IntOr(map[string]string{GitPollIntervalS: "0"}, GitPollIntervalS, 7) != 0 {
		t.Error("IntOr must default only when unset or unreadable")
	}
}

func TestEnumAndText(t *testing.T) {
	if Enum(map[string]string{GitDiffMode: " MAIN_HEAD "}, GitDiffMode) != "main_head" || Enum(nil, GitDiffMode) != "branch_point" || Enum(map[string]string{GitDiffMode: "x"}, GitDiffMode) != "branch_point" {
		t.Error("Enum")
	}
	if Text(map[string]string{DefaultPromptWorker: "  hi "}, DefaultPromptWorker) != "hi" || TextFamily(map[string]string{"default_model_claude": " m "}, "default_model_claude") != "m" {
		t.Error("Text")
	}
}

func TestUndeclaredKeyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("reading an undeclared key must panic")
		}
	}()
	Bool(nil, "no_such_setting")
}

func TestSpecsAreSane(t *testing.T) {
	for _, s := range All() {
		if s.Doc == "" {
			t.Errorf("%s has no doc", s.Key)
		}
		if s.Kind == KindEnum && len(s.Values) == 0 {
			t.Errorf("%s: an enum with no values", s.Key)
		}
		if s.Kind == KindBool && s.Default != "true" && s.Default != "false" {
			t.Errorf("%s: a bool's default is %q", s.Key, s.Default)
		}
	}
}
