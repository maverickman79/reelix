# jellyfin-web served against Reelix.
#
# The bundle is lifted from the official server image rather than built or
# vendored: it is 60MB of compiled JavaScript, it belongs to Jellyfin, and
# nothing about it should enter this repository. The tag is pinned to the same
# 10.11.8 the capture harness uses, which is the compatibility target.
#
# This is a client, not a dependency. It is never linked, imported, or shipped
# with Reelix, and no code from it is read for implementation guidance — only
# its observable HTTP behaviour, per the clean-room rule in CLAUDE.md.
FROM jellyfin/jellyfin:10.11.8 AS bundle

FROM nginx:1.27-alpine
COPY --from=bundle /jellyfin/jellyfin-web /usr/share/nginx/html/web
COPY nginx.conf /etc/nginx/templates/default.conf.template
