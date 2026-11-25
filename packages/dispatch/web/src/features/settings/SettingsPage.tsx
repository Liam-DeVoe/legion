import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { PageHeader, SectionHeader } from "../../components/Layout";
import { GrantsSection } from "../credentials/GrantsSection";
import { credentialPendingQuery } from "../credentials/pending";
import { SignInsSection } from "../credentials/SignInsSection";
import { DeliverySettingsForm } from "../delivery/DeliverySettingsForm";
import { IssuePagePreference } from "../issue-page/IssuePagePreference";
import { useDocumentTitle } from "../shell/useDocumentTitle";
import { AgentTokensSection } from "./AgentTokensSection";
import { ArchitectureSourcesSection } from "./ArchitectureSourcesSection";
import { settingsSectionCard, settingsSectionGap } from "./classes";
import { LegionDaemonsSection } from "./LegionDaemonsSection";
import { ProjectsSection } from "./ProjectsSection";

export function SettingsPage(): ReactNode {
  useDocumentTitle("Settings · Dispatch");
  // `null` is a Dispatch with no secrets broker, where Live grants has nothing to list.
  const credentialsAvailable = useQuery(credentialPendingQuery());

  return (
    <section className="max-w-4xl">
      <PageHeader
        description="Manage the projects, the agent tokens and the settings of this Dispatch."
        title="Settings"
      />
      <div className={settingsSectionGap}>
        <div className={settingsSectionCard}>
          <ProjectsSection />
        </div>
        <div className={settingsSectionCard}>
          <AgentTokensSection />
        </div>
        {credentialsAvailable.isPending || credentialsAvailable.data === null ? null : (
          <>
            <div className={settingsSectionCard}>
              <GrantsSection />
            </div>
            <div className={settingsSectionCard}>
              <SignInsSection />
            </div>
          </>
        )}
        <div className={settingsSectionCard}>
          <ArchitectureSourcesSection />
        </div>
        <section aria-labelledby="delivery-timeline-heading" className={settingsSectionCard}>
          <SectionHeader
            description="Which repository deploys, and whose merged pull requests the Delivery page counts. Saving verifies the Dispatch GitHub App can read the deploy repository."
            id="delivery-timeline-heading"
            title="Delivery timeline"
          />
          <DeliverySettingsForm />
        </section>
        <div className={settingsSectionCard}>
          <IssuePagePreference />
        </div>
        <div className={settingsSectionCard}>
          <LegionDaemonsSection />
        </div>
      </div>
    </section>
  );
}
