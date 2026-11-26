"use client";

import { uiCopy } from "@/lib/glossary";
import { PasswordForm } from "@/components/password-form";
import { Badge, Card, Grid, KV, Mono, PageHeader } from "@/components/ui";
import { useApp } from "@/lib/store";
import { Timestamp } from "@/components/timestamp";

export default function ProfilePage() {
  const { userName, isAdmin, auth } = useApp();

  return (
    <>
      <PageHeader title={uiCopy.profile} helpTopic="authority" />
      <Grid cols={2}>
        <Card title="账户">
          <KV
            cols={1}
            items={[
              ["用户名", <Mono key="u">{userName}</Mono>],
              ["角色", isAdmin ? <Badge key="r" tone="brand">管理员</Badge> : <Badge key="r">普通用户</Badge>],
              ["当前会话", auth.session ? <><Timestamp value={auth.session.created_at} /> 登录 · <Timestamp value={auth.session.idle_expires_at} /> 闲置到期 · <Timestamp value={auth.session.absolute_expires_at} /> 最迟到期</> : "未知"],
            ]}
          />
        </Card>
        <Card title="修改密码">
          <PasswordForm />
        </Card>
      </Grid>
    </>
  );
}
