import type { NextConfig } from "next";

const config: NextConfig = {
  output: "standalone",
  // Allow the dashboard to be deployed behind a subpath if needed.
  // Set BASE_PATH env var at build time.
  basePath: process.env.BASE_PATH ?? "",
  turbopack: {
    // Resolve the monorepo workspace-root warning by pinning to the dashboard dir.
    root: __dirname,
  },
};

export default config;
