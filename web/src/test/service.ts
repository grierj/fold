import { afterAll, beforeAll } from "vitest";

export function stubService<Service>(service: Service, makeReplacements: () => NoInfer<Partial<Service>>) {
  const originals = new Map<keyof Service, Service[keyof Service]>();

  beforeAll(() => {
    const replacements: Partial<Service> = makeReplacements();

    for (const key in replacements) {
      const replacement = replacements[key];

      if (replacement === undefined) continue;

      originals.set(key, service[key]);
      service[key] = replacement;
    }
  });

  afterAll(() => {
    for (const [key, value] of originals) service[key] = value;
  });
}
