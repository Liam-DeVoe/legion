import type { ReactNode } from "react";
import { useNavigate } from "react-router-dom";

import { Tabs } from "../../components/Tabs";
import { buildIssuePath, type IssueTab } from "../refs/routes";
import { issueTabs } from "./issue-tab-list";

/** The IssuePage tablist keeps its active view in the canonical issue route. `artifactCount` is
 *  how many rows the Artifacts tab lists - every artifact the issue has, its primary document
 *  included - so the badge and the list a reader checks it against say the same number. */
export function IssueTabs({
  activeTab,
  artifactCount,
  issueKey,
  onBeforeTabChange,
  toolbar,
}: {
  activeTab: IssueTab;
  artifactCount: number;
  issueKey: string;
  onBeforeTabChange: (current: IssueTab) => void;
  toolbar?: ReactNode;
}): ReactNode {
  const navigate = useNavigate();

  return (
    // The frame gives every tab's body the same 16 px under the tab bar, so no body sets a top
    // margin of its own. The Spec's toolbar is a row of its own under the bar, at every width: as
    // the tablist's trailing item it shared the bar only on the widest screens and wrapped into
    // the bar's box, above its rule, everywhere else.
    <div className="mb-4" data-testid="issue-tabs">
      <Tabs
        activeTab={activeTab}
        ariaLabel="Issue detail"
        compact
        idPrefix="issue"
        onBeforeTabChange={onBeforeTabChange}
        onSelect={(tab) => navigate(buildIssuePath({ key: issueKey, kind: tab }))}
        tabs={issueTabs(artifactCount)}
      />
      {toolbar === undefined || toolbar === null ? null : <div className="mt-2">{toolbar}</div>}
    </div>
  );
}
