type Control = {
  el: string;
  name: string;
};

type Target = Control & {
  w: number;
  h: number;
};

export function measure(): {
  overflow: number;
  cutOff: Control[];
  clipped: { id: string; label: string; text: number; room: number }[];
  under24: Target[];
  under44: Target[];
};
