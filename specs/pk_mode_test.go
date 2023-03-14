package specs

import (
	"testing"
)

func TestPKModeFromString(t *testing.T) {
	var pkMode PKMode
	if err := pkMode.UnmarshalJSON([]byte(`"cq-id"`)); err != nil {
		t.Fatal(err)
	}
	if pkMode != PKModeCQID {
		t.Fatalf("expected PKModeCQID, got %v", pkMode)
	}
	if err := pkMode.UnmarshalJSON([]byte(`"composite-keys"`)); err != nil {
		t.Fatal(err)
	}
	if pkMode != PKModeCompositeKeys {
		t.Fatalf("expected PKModeCompositeKeys, got %v", pkMode)
	}
}

func TestPKMode(t *testing.T) {
	for _, pkModeStr := range pkModeStrings {
		pkMode, err := PKModeFromString(pkModeStr)
		if err != nil {
			t.Fatal(err)
		}
		if pkModeStr != pkMode.String() {
			t.Fatalf("expected:%s got:%s", pkModeStr, pkMode.String())
		}
	}
}

func TestPKModeMarshalJSON(t *testing.T) {
	pkMode := PKModeCQID
	if pkModeStr, err := pkMode.MarshalJSON(); err != nil {
		t.Fatal(err)
	} else if string(pkModeStr) != `"cq-id"` {
		t.Fatalf("expected:\"cq-id\" got:%s", string(pkModeStr))
	}

	pkMode = PKModeCompositeKeys
	if pkModeStr, err := pkMode.MarshalJSON(); err != nil {
		t.Fatal(err)
	} else if string(pkModeStr) != `"composite-keys"` {
		t.Fatalf("expected:\"cq-id\" got:%s", string(pkModeStr))
	}

}
