<script>
	let { children } = $props();
</script>
<nav><a href="/docs/">home</a> <a href="/docs/about">about</a> <a href="/docs/dyn">dyn</a></nav>
{@render children()}
<style>nav { color: rebeccapurple; }</style>
