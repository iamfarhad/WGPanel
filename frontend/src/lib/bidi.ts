/** Isolate a technical run (a size like "3.4 GB", a ratio like "3 / 250") as
 * left-to-right so right-to-left text around it cannot reorder its parts. */
export function ltr(text: string): string {
  return `⁦${text}⁩`
}
