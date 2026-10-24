import { building } from '$app/environment';

// a hook on every request it runs for: its header marks the response, and
// /dyn needs a session (prerendering crawls to it: it's let through then)
export async function handle({ event, resolve }) {
	if (!building && event.url.pathname.endsWith('/dyn') && !event.cookies.get('session')) {
		return new Response('no session', { status: 401, headers: { 'x-hook': '1' } });
	}
	const r = await resolve(event);
	r.headers.set('x-hook', '1');
	return r;
}
