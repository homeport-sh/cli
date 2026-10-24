// middleware on every request it runs for: its header marks the response,
// and /dyn needs a session
export const onRequest = async (ctx, next) => {
	if (!ctx.isPrerendered && ctx.url.pathname.replace(/\/$/, '').endsWith('/dyn') && !ctx.cookies.get('session')) {
		return new Response('no session', { status: 401, headers: { 'x-mw': '1' } });
	}
	const r = await next();
	r.headers.set('x-mw', '1');
	return r;
};
