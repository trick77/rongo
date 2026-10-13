package auth

import kitauth "github.com/trick77/ship/kit/auth"

// Claims is the verified identity an OIDC login yields: kit's flow checks
// state, PKCE, the ID token and its nonce. Groups is read straight off the ID
// token and never from the UserInfo endpoint, which is why Authelia's client
// entry needs claims_policy 'legacy' — see ../authelia/conf/configuration.yaml.
// Getting that wrong is silent: the login succeeds and the group-derived admin
// flag quietly disappears.
type Claims = kitauth.Claims
