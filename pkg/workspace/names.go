package workspace

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

// nameAdjectives and nameNouns feed RandomName. Every word is lowercase
// ASCII so generated names pass ValidateName and survive DefaultHostname
// unchanged.
var nameAdjectives = []string{
	"agile", "amber", "bold", "brave", "bright", "brisk", "calm", "clever",
	"cosmic", "crisp", "curious", "daring", "deft", "eager", "early", "fleet",
	"fond", "gentle", "glad", "golden", "handy", "happy", "hardy", "keen",
	"kind", "lively", "lucid", "lunar", "merry", "mighty", "nimble", "noble",
	"patient", "plucky", "polite", "proud", "quick", "quiet", "rapid", "solid",
	"steady", "sunny", "swift", "tidy", "trusty", "vivid", "wise", "witty",
}

var nameNouns = []string{
	"badger", "beaver", "bison", "crane", "curlew", "dingo", "donkey", "falcon",
	"ferret", "finch", "gecko", "gibbon", "grouse", "heron", "hyrax", "ibex",
	"jackal", "kestrel", "koala", "lemur", "linnet", "lynx", "macaw", "marmot",
	"marten", "merlin", "mole", "moose", "narwhal", "ocelot", "orca", "osprey",
	"otter", "panda", "pika", "plover", "puffin", "quokka", "raven", "seal",
	"shrew", "stoat", "swift", "tapir", "vole", "walrus", "wombat", "wren",
}

// RandomName mints a human-readable workspace name: prefix, adjective, noun,
// and a short random suffix for collision avoidance (e.g. "run-brave-otter-4f9c").
// The result always passes ValidateName.
func RandomName(prefix string) string {
	const suffixAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	suffix := make([]byte, 4)
	for i := range suffix {
		suffix[i] = suffixAlphabet[randomIndex(len(suffixAlphabet))]
	}
	return fmt.Sprintf("%s-%s-%s-%s",
		prefix,
		nameAdjectives[randomIndex(len(nameAdjectives))],
		nameNouns[randomIndex(len(nameNouns))],
		suffix)
}

func randomIndex(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return int(time.Now().UnixNano() % int64(n))
	}
	return int(v.Int64())
}
