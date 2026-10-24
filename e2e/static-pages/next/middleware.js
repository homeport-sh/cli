import { NextResponse } from "next/server";

// marks every response it runs for: the check that the assets it skips are
// served without it
export function middleware() {
  const res = NextResponse.next();
  res.headers.set("x-mw", "1");
  return res;
}

export const config = { matcher: ["/((?!_next/static|favicon.ico).*)"] };
