# Response headers

An app's response headers are set in `homeport.yaml`, or in the app's
Settings on the dashboard. homeport adds them to every answer the app
gives. That includes static sites, apps woken from sleep, and the page
shown while an app is paused.

```yaml
headers:
  - path: /shopify/embedded
    set:
      Content-Security-Policy: "frame-ancestors https://admin.shopify.com https://*.myshopify.com"
      Strict-Transport-Security: max-age=31536000
      X-Content-Type-Options: nosniff
      Referrer-Policy: same-origin
      Permissions-Policy: interest-cohort=()
  - path: /
    set:
      Strict-Transport-Security: max-age=31536000
      X-Content-Type-Options: nosniff
      X-Frame-Options: DENY
      Referrer-Policy: same-origin
      Permissions-Policy: interest-cohort=()
    remove: [X-Powered-By]
```

- **`path` is a prefix on segment boundaries.** `/shopify/embedded`
  matches `/shopify/embedded` and `/shopify/embedded/app`, but not
  `/shopify/embeddedx`. `/` matches every path. Matching ignores case and
  repeated slashes.
- **The longest matching path wins, and only its rule applies.** Rules
  don't merge. Above, `/shopify/embedded` gets no `X-Frame-Options`,
  because only `/`'s rule sets one. A header both rules need goes in both
  (YAML anchors save the repetition).
- **What you set replaces the app's own value** for that header. The app's
  value is never sent beside it. A header your rule doesn't name passes
  through as the app sent it.
- **`remove`** takes a header the app sends out of the answer.
- **Settings win over `homeport.yaml`.** When the app's Settings have any
  header rules, they replace the file's entirely. Saving them applies them
  to what's live within seconds, with no build. A change to
  `homeport.yaml` applies with the next deploy. That includes a deploy
  from your own CI, which reads `homeport.yaml` from the commit it
  deploys.

## Limits

- 16 rules; 32 headers set and 16 removed per rule; 48 KiB in all.
- A path is `/`, or segments of letters, digits and `. _ ~ -`. There are
  no wildcards, no `.` or `..` segments and no trailing `/`, and paths
  are at most 256 characters.
- A name is a letter followed by letters, digits and `-`, at most 64
  characters.
- A value is one line of printable ASCII, up to 8 KiB, without `` ` ``,
  `{` or `}`. Quotes and backslashes are fine, as in
  `geolocation=(self "https://a.example")`.
- These names can't be set or removed, because the app, the protocol or
  homeport owns them: `Set-Cookie`, `Location`, `Content-Length`,
  `Transfer-Encoding`, `Connection`, `Keep-Alive`, `Proxy-Connection`,
  `TE`, `Trailer`, `Upgrade`, `Content-Encoding`, `Vary`,
  `Content-Range`, `Accept-Ranges`, `ETag`, `Last-Modified`, `Date`,
  `Alt-Svc`, `X-Robots-Tag` (a preview is never indexed), `X-Origin-Auth`
  and `X-Homeport-*`. `Server` and `Via` can be removed, but not set.

## Where they don't apply

- A 502 or 504 that homeport answers itself, because the app isn't
  answering, carries none of them.
- Answers that aren't the app's: a redirect from one of the app's
  domains to another, a private preview's sign-in, and a name homeport
  doesn't serve.
