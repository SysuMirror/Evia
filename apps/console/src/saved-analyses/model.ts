/** Mirrors the server-side reporting.maxCatalogSearchRunes boundary. */
export const maxSavedAnalysisSearchRunes = 128;

/**
 * Normalizes the title search box before the term reaches the API: trims
 * surrounding whitespace and clamps to the server rune limit so an over-long
 * local input can never trigger a guaranteed 400. A blank result means "no
 * filter", which the history hook encodes by omitting the q parameter.
 */
export function normalizeSavedAnalysisSearch(input: string): string {
  const trimmed = input.trim();
  const runes = [...trimmed];
  return runes.length > maxSavedAnalysisSearchRunes
    ? runes.slice(0, maxSavedAnalysisSearchRunes).join('')
    : trimmed;
}
