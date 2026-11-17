import { fileURLToPath } from "node:url";

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "standalone",
  reactStrictMode: true,
  transpilePackages: ["@sub-pool/contracts"],
  outputFileTracingRoot: fileURLToPath(new URL("..", import.meta.url)),
  async redirects() {
    return [
      { source: "/", destination: "/console", permanent: false },
      {
        source: "/:page(accounts|api-keys|backup|balances|connection|dispatch|estimates|help|maintenance|notifications|observations|pools|pricing|profile|security|temporary|usage|users)",
        destination: "/console/:page",
        permanent: true,
      },
    ];
  },
};
export default nextConfig;
