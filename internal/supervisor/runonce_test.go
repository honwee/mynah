package supervisor

import "testing"

// One-shot names are the second place a caller-supplied string becomes a
// container identity (pool.go is the first). The namespace prefix is what keeps
// them from ever naming a service or a pool worker, so the check has to reject
// anything that tries to leave it.
func TestValidateOneshotNameRejectsEverythingOutsideItsNamespace(t *testing.T) {
	for _, bad := range []string{
		"",
		"mynah-tts",             // a fixed service
		"mynah-musetalk-1",      // a pool worker
		"mynah-cored",           // the console's own container
		"mynah-oneshot-",        // empty token
		"mynah-oneshot-../etc",  // traversal
		"mynah-oneshot-a/b",     // path separator
		"mynah-oneshot-UPPER",   // case
		"mynah-oneshot-a_b",     // underscore is not in the token charset
		"oneshot-x",                   // missing prefix
		"mynah-oneshot-" + string(make([]byte, 40)),
	} {
		if err := validateOneshotName(bad); err == nil {
			t.Errorf("validateOneshotName(%q) accepted a name it must refuse", bad)
		}
	}
	for _, good := range []string{"mtbake-1", "mtframes-42", "a", "job-99999"} {
		if err := validateOneshotName(OneshotName(good)); err != nil {
			t.Errorf("validateOneshotName(%q) = %v, want accepted", good, err)
		}
	}
}

// A one-shot name must not collide with the pool namespace in either
// direction: each validator has to reject the other's names, or a bake could
// remove a live worker (or a pool resize could kill a running bake).
func TestOneshotAndPoolNamespacesAreDisjoint(t *testing.T) {
	oneshot := OneshotName("mtbake-1")
	if _, _, err := validatePoolName(oneshot); err == nil {
		t.Errorf("the pool validator accepted the one-shot name %q", oneshot)
	}
	pool := PoolName("musetalk", 3)
	if err := validateOneshotName(pool); err == nil {
		t.Errorf("the one-shot validator accepted the pool name %q", pool)
	}
}
