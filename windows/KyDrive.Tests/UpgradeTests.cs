namespace KyDrive.Tests;

// Q5: only a core reporting an older release than the shell is replaced.
public class UpgradeTests
{
    [Theory]
    [InlineData("0.5.9", true)]
    [InlineData("0.6.0", false)]
    [InlineData("0.10.0", false)]
    [InlineData("", false)]
    [InlineData(null, false)]
    public void OnlyAnOlderCoreIsReplaced(string? core, bool older) =>
        Assert.Equal(older, Upgrade.Older(core, new Version(0, 6, 0, 0)));
}
