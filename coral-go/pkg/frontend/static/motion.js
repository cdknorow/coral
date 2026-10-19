/* The scroll behaviour for an explicit scrollIntoView / scrollTo: smooth, except for a person who asked for reduced motion (#5140). */
export const scrollBehavior = () => (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth');
