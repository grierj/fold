import facts from "../../../deploy/digitalocean/qc1/app-facts.json";

// What the QC-1 harness drives in the browser: selectors, labels and copy. The
// harness reads them from this file (deploy/digitalocean/qc1/app-facts.json,
// "web"); tests built on QC1_WEB hold them equal to what the components render,
// so a rename fails beside the component and not in a deploy check.
export interface QC1WebFacts {
  displayPage: number;
  reasonRows: string;
  reasonsNav: string;
  reasonText: string;
  reasonIdAttr: string;
  reasonHeadingAttr: string;
  progressSelector: string;
  progressPattern: string;
  progressChoose: string;
  radioRemoveValue: string;
  copy: { notChosen: string; changed: string; checking: string; historyUnavailable: string };
  navOfLoading: string;
  navOfDone: string;
  labels: {
    keepText: string;
    blankText: string;
    goToReason: string;
    goButton: string;
    nextUnchosen: string;
    nextChanged: string;
    deleteAccount: string;
    checkAgain: string;
    usersSearch: string;
    moreForPrefix: string;
    menuDelete: string;
  };
}

export const QC1_WEB: QC1WebFacts = facts.web;
