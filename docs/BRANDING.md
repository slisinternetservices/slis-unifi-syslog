# SLIS wordmark

The original SLIS vector paths and INTERNET SERVICES lettering were extracted from
the supplied letterhead and rendered against transparency. The source letterhead
and its address/banking details are not part of the published assets.

- `web/public/slis-logo-light.png`: blue wordmark, original dark subtitle.
- `web/public/slis-logo-dark.png`: blue wordmark, light subtitle for dark panels.
- Both images: 1189 x 367 px, RGBA PNG, around 65 KB, below the 512 KB upload limit.
- The image proportions are retained. The built-in sign-in wordmark is 220 px wide;
  the sidebar wordmark is 140 px wide, both constrained to their containers.

## Apply immediately to an existing server

No server upgrade is needed to upload the new branding assets:

1. Download the two PNG files from this repository's `web/public` directory.
2. Sign in as administrator and open Administration > Branding.
3. Upload slis-logo-light.png under Logo for light mode.
4. Upload slis-logo-dark.png under Logo for dark mode.
5. Check the preview in both modes, then sign out to inspect the sign-in screen.

The existing image upload sizing also preserves the original image proportions.
This affects the menu, sign-in screen and supported branded e-mails. The new
built-in default is included when the application is next built from this source.
