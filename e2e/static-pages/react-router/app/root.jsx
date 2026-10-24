import { Links, Meta, Outlet, Scripts } from "react-router";
import "./site.css";

// marks every document the app renders: the check that its assets are
// served without it
export function headers() {
  return { "x-hook": "1" };
}

export default function Root() {
  return (
    <html lang="en">
      <head>
        <Meta />
        <Links />
      </head>
      <body>
        <Outlet />
        <Scripts />
      </body>
    </html>
  );
}
