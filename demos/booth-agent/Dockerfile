# Visitor-facing agent chat: spawns one CCB headless process per session and
# streams NDJSON->SSE. CCB's dist (built separately, see demos/README.md) is
# mounted read-only at /ccb at runtime.
FROM node:22-alpine
RUN apk add --no-cache bash
COPY server.js index.html /appsrv/
EXPOSE 80
CMD ["node", "/appsrv/server.js"]
