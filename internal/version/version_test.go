package version

import "testing"

func TestNormalizeDropsTheModuleVersionPrefix(t *testing.T) {
	// A Go module version always carries a leading "v"; the spec's version
	// string does not (the reference build reports "Mackup 0.11.1").
	tests := map[string]string{
		"v0.11.1":                       "0.11.1",
		"0.11.1":                        "0.11.1",
		"v0.0.0-20260902050000-32eaf47": "0.0.0-20260902050000-32eaf47",
		"":                              "",
	}
	for in, want := range tests {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStringNormalizesTheStampedValue(t *testing.T) {
	original := value
	t.Cleanup(func() { value = original })

	value = "v1.2.3"
	if got := String(); got != "1.2.3" {
		t.Errorf("String() = %q, want %q", got, "1.2.3")
	}
	if got := Banner(); got != "Mackup 1.2.3" {
		t.Errorf("Banner() = %q, want %q", got, "Mackup 1.2.3")
	}
}

func TestStringFallsBackForAnUninstalledTree(t *testing.T) {
	original := value
	t.Cleanup(func() { value = original })

	value = ""
	if got := String(); got != Fallback {
		t.Errorf("String() = %q, want the fallback token %q", got, Fallback)
	}
}
