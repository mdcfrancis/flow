package macro
import ("strings";"testing")
func TestFormat(t *testing.T){
	src := `(cell run-tick (set orb_0_x (get orb_0_x)) (set orb_0_y (get orb_0_y)) (set orb_1_x (i32.add (get orb_1_x) (get orb_1_vx))) (i32.const 0))`
	out := Format(src)
	t.Logf("\n%s", out)
	if !strings.Contains(out, "\n") { t.Fatal("long form should be multiline") }
	if strings.Contains(out, "(get orb_0_x)") == false { t.Error("small forms should stay inline") }
	// non-sexpr (forth) passes through
	if Format("ball_x ball_vx + -> ball_x") != "ball_x ball_vx + -> ball_x" {
		t.Error("forth should pass through unchanged")
	}
}
