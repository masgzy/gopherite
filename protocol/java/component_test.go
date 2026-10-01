package java

import (
	"strings"
	"testing"

	"github.com/masgzy/gopherite/protocol"
)

// TestLoginDisconnectWireFormat pins the login-phase disconnect wire
// form: a VarInt-length string holding compact JSON — NOT the NBT tag
// used by configuration/play disconnects. Vanilla 26.2 still ships the
// JSON form here (ClientboundLoginDisconnectPacket uses lenientJson);
// captured proof lives in research/ours-dump/vanilla_login_disconnect.bin.
// The original regression: NBT bytes where the client expects JSON made
// real clients fail with "Failed to decode packet 'login_disconnect'".
func TestLoginDisconnectWireFormat(t *testing.T) {
	const key = "multiplayer.disconnect.unverified_username"
	w := protocol.NewWriter()
	WriteLoginDisconnect(w, key)

	r := protocol.NewReader(w.Bytes())
	got, err := r.String(300)
	if err != nil {
		t.Fatalf("read back as string: %v", err)
	}
	if want := `{"translate":"` + key + `"}`; got != want {
		t.Fatalf("payload %q, want %q", got, want)
	}
	if strings.HasPrefix(got, "\x0a") {
		t.Fatal("login disconnect must not start with an NBT root tag")
	}
}

// TestComponentNBTStringWidth checks that NBT components carry
// unsigned-short-length TAG_Strings, not VarInt lengths — a mixed-up
// width shifts every later field and breaks strict client parsers.
func TestComponentNBTStringWidth(t *testing.T) {
	w := protocol.NewWriter()
	WriteTextComponent(w, "hello")

	r := protocol.NewReader(w.Bytes())
	if root, _ := r.Byte(); root != 0x0A {
		t.Fatalf("root 0x%x", root)
	}
	if et, _ := r.Byte(); et != 0x08 {
		t.Fatalf("field type 0x%x", et)
	}
	nameLen, err := r.Uint16()
	if err != nil {
		t.Fatal(err)
	}
	if nameLen != 4 {
		t.Fatalf("field name length %d, want 4 (u16 width)", nameLen)
	}
	raw, err := r.FixedBytes(int(nameLen))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "text" {
		t.Fatalf("field name %q", string(raw))
	}
	valLen, _ := r.Uint16()
	if valLen != 5 {
		t.Fatalf("value length %d, want 5", valLen)
	}
	raw, err = r.FixedBytes(int(valLen))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello" {
		t.Fatalf("value %q", string(raw))
	}
	if end, _ := r.Byte(); end != 0x00 {
		t.Fatal("missing TAG_End")
	}
}
