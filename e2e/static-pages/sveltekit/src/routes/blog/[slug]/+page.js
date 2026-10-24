export const prerender = true;
export const entries = () => [{ slug: 'one' }, { slug: 'two' }];
export const load = ({ params }) => ({ slug: params.slug });
