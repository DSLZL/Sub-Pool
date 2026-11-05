import { fileURLToPath } from "node:url";

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  transpilePackages: ["@sub-pool/contracts"],
  outputFileTracingRoot: fileURLToPath(new URL("..", import.meta.url)),
};
export default nextConfig;
