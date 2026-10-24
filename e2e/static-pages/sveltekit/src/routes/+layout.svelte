<script>
	let { children } = $props();
</script>
<nav><a href="/">home</a> <a href="/about">about</a> <a href="/dyn">dyn</a></nav>
{@render children()}
<style>nav { color: rebeccapurple; }</style>
