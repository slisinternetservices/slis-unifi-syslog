package update

import (
	"strconv"
	"strings"
)

// Newer reports whether version a is higher than b, following semantic
// versioning: 1.2.0 > 1.2.0-rc.1 > 1.2.0-beta.2 > 1.2.0-beta.1.
func Newer(a, b string) bool { return Compare(a, b) > 0 }

// Compare returns -1, 0 or 1 when a is lower than, equal to or higher than b.
// A leading "v" and build metadata ("+...") are ignored.
func Compare(a, b string) int {
	ca, pa := split(a)
	cb, pb := split(b)
	for i := 0; i < 3; i++ {
		if ca[i] != cb[i] {
			return sign(ca[i] - cb[i])
		}
	}
	switch {
	case pa == "" && pb == "":
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	ia, ib := strings.Split(pa, "."), strings.Split(pb, ".")
	for i := 0; i < len(ia) && i < len(ib); i++ {
		na, ea := strconv.Atoi(ia[i])
		nb, eb := strconv.Atoi(ib[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return sign(na - nb)
			}
		case ea == nil: // numeric identifiers sort before alphanumeric ones
			return -1
		case eb == nil:
			return 1
		default:
			if c := strings.Compare(ia[i], ib[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(ia) - len(ib))
}

// IsPrerelease reports whether v carries a pre-release suffix such as
// "-beta.1". Those versions are only offered on the test channel.
func IsPrerelease(v string) bool {
	_, pre := split(v)
	return pre != ""
}

func split(v string) (core [3]int, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		core[i], _ = strconv.Atoi(p)
	}
	return core, pre
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
