// a catch-all security headers rule and a :param one go with the assets the
// host answers (routes.json); its redirects are the host's to answer
export default {
  output: "standalone",
  async headers() {
    return [
      {
        source: "/(.*)",
        headers: [
          { key: "X-Frame-Options", value: "DENY" },
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
          { key: "Content-Security-Policy", value: "default-src 'self' https:" },
        ],
      },
      { source: "/_next/static/chunks/:file", headers: [{ key: "X-File", value: ":file" }, { key: "Referrer-Policy", value: "no-referrer" }] },
    ];
  },
  async redirects() {
    return [
      { source: "/", destination: "/dashboard", permanent: false },
      { source: "/old/:slug", destination: "/new/:slug", permanent: true },
      { source: "/docs/:path*", destination: "/guide/:path*", statusCode: 301 },
    ];
  },
};
