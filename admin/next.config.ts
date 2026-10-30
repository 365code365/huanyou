import type { NextConfig } from 'next'

// Static export: `next build` writes plain HTML/JS to out/, which
// scripts/copy-dist.mjs copies into the Go API (go:embed). No Node server in production.
const isDev = process.env.NODE_ENV === 'development'

const config: NextConfig = {
  basePath: '/admin',
  images: { unoptimized: true },
  reactStrictMode: true,
  ...(isDev
    ? {
        // `npm run dev`: proxy /admin/api to a local vpn-api.
        async rewrites() {
          return [
            {
              source: '/api/:path*',
              destination: `${process.env.VPN_API ?? 'http://127.0.0.1:18090'}/admin/api/:path*`,
            },
          ]
        },
      }
    : { output: 'export' as const }),
}

export default config
