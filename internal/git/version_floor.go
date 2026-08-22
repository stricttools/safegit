package git

import (
	"context"
	"sync"

	"github.com/smm-h/safegit/internal/gitversion"
)

// The installed git's version, asked for once per process.
//
// The question is worth caching because the answer cannot change while safegit
// runs, and the sites that ask are on paths that would otherwise fork `git
// --version` before every conflicted-path attribute lookup. A FAILURE to ask is
// cached too, deliberately: a git binary that cannot report its version is not
// going to start reporting it mid-invocation, and re-asking would multiply one
// broken installation into one failure message per call site.
var (
	versionOnce  sync.Once
	cachedVer    gitversion.Version
	cachedVerErr error
)

// RequireFeature refuses when the installed git is older than the floor a
// declared feature carries, and returns nil otherwise.
//
// It is the production entry point to internal/gitversion: a command that is
// about to run git syntax with a version floor calls this FIRST, so an operator
// on an older git is told which git feature safegit needs and which version
// introduced it -- rather than being handed git's own "unknown option" from
// somewhere in the middle of a conclusion.
//
// The version is read once per process (see above), so a caller may call this
// on a hot path.
func RequireFeature(ctx context.Context, f gitversion.Feature) error {
	versionOnce.Do(func() {
		cachedVer, cachedVerErr = Version(ctx)
	})
	if cachedVerErr != nil {
		return cachedVerErr
	}
	return gitversion.Require(cachedVer, f)
}
