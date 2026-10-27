import type { ButtonHTMLAttributes, ReactNode, Ref } from "react";
import { Link } from "react-router-dom";

// The one button (ENFORCEMENT.md A). Color comes only from `variant`; there
// is no className, so a page can't recolor or resize it with overrides.
// `layout` takes placement classes such as "w-full" or "self-start".
export type ButtonVariant = "primary" | "secondary" | "danger" | "danger-outline" | "ghost" | "link";

type Props = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className" | "style"> & {
  variant: ButtonVariant;
  ref?: Ref<HTMLButtonElement>;
  // "sm" (36px tall) is for table rows only; everything else is 44px.
  size?: "md" | "sm";
  layout?: string;
};

const buttonClass = (variant: ButtonVariant, size: "md" | "sm", layout?: string) =>
  [`btn-${variant}`, size === "sm" ? "min-h-9 px-3 text-sm" : "", layout ?? ""].filter(Boolean).join(" ");

export function Button({ variant, size = "md", layout, type = "button", ...rest }: Props) {
  return <button type={type} className={buttonClass(variant, size, layout)} {...rest} />;
}

export function ButtonLink({
  to,
  variant,
  size = "md",
  layout,
  reloadDocument,
  external,
  children,
}: {
  to: string;
  // An address outside Fold (a video call): a plain anchor that opens in
  // a new tab. The caller adds the "(opens in a new tab)" text.
  external?: boolean;
  // A server URL, not an SPA route (a .ics feed): the browser loads it.
  reloadDocument?: boolean;
  variant: ButtonVariant;
  size?: "md" | "sm";
  layout?: string;
  children: ReactNode;
}) {
  if (external) {
    return (
      <a href={to} target="_blank" rel="noopener noreferrer" className={buttonClass(variant, size, layout)}>
        {children}
      </a>
    );
  }
  return (
    <Link to={to} reloadDocument={reloadDocument} className={buttonClass(variant, size, layout)}>
      {children}
    </Link>
  );
}
