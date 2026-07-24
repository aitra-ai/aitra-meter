import type { NextConfig } from "next";

const config: NextConfig = {
  // Standalone output is for the Docker image; Vercel manages its own output.
  output: process.env.VERCEL ? undefined : "standalone",
  // Allow the dashboard to be deployed behind a subpath if needed.
  // Set BASE_PATH env var at build time.
  basePath: process.env.BASE_PATH ?? "",
  env: { NEXT_PUBLIC_BASE_PATH: process.env.BASE_PATH ?? "" },
  turbopack: {
    // Resolve the monorepo workspace-root warning by pinning to the dashboard dir.
    root: __dirname,
  },
};

export default config;
