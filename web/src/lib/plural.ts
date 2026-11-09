// "1 date", "8 dates"; pass the plural for the odd nouns ("1 person", "3 people").
export function count(n: number, singular: string, plural = `${singular}s`): string {
  return `${n} ${n === 1 ? singular : plural}`;
}

export const people = (n: number): string => count(n, "person", "people");
