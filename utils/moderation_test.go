package utils

import "testing"

func TestT2KeywordCheckRejectsMultipleSpamSignals(t *testing.T) {
	passed, reason := T2KeywordCheck("Tai lieu nay yeu cau mua ngay de nhan gia re nhat trong hom nay")
	if passed {
		t.Fatalf("T2KeywordCheck passed, want rejection")
	}
	if reason == "" {
		t.Fatalf("T2KeywordCheck reason is empty")
	}
}

func TestT2KeywordCheckAllowsNeutralText(t *testing.T) {
	passed, reason := T2KeywordCheck("Noi dung hoc tap ve giai tich, dao ham, tich phan va ung dung trong vat ly")
	if !passed {
		t.Fatalf("T2KeywordCheck rejected neutral text: %s", reason)
	}
}
