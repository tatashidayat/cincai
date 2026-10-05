// OAuth provider packs — blank-import so init() registers with credential/oauth/pack.
package link

import (
	_ "github.com/subosito/cincai/internal/oauth-providers/openai"
	_ "github.com/subosito/cincai/internal/oauth-providers/xai"
)
